package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func testKey(t *testing.T) (ssh.Signer, []byte) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return signer, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

func TestConfigurationRejectsExposureAndUnverifiedHost(t *testing.T) {
	key, _ := testKey(t)
	valid := config{
		RelayAddress: "relay.example.com:22", RelayUser: "tunnel", PrivateKeyFile: "key",
		RelayHostKeySHA256: ssh.FingerprintSHA256(key.PublicKey()),
		TargetAddress:      "192.168.1.150:22", RemoteListen: "127.0.0.1:22022",
	}
	if err := valid.validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*config){
		func(c *config) { c.RemoteListen = "0.0.0.0:22022" },
		func(c *config) { c.RemoteListen = "example.com:22022" },
		func(c *config) { c.RemoteListen = "127.0.0.1:0" },
		func(c *config) { c.TargetAddress = "192.168.1.150:70000" },
		func(c *config) { c.RelayHostKeySHA256 = "" },
		func(c *config) { c.RelayHostKeySHA256 = "SHA256:unverified" },
		func(c *config) { c.KnownHostsFile = "known_hosts" },
	} {
		cfg := valid
		mutate(&cfg)
		if err := cfg.validate(); err == nil {
			t.Fatalf("invalid configuration accepted: %+v", cfg)
		}
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "tunnel.json")
	data, _ := json.Marshal(valid)
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadConfig(file)
	if err != nil || loaded.PrivateKeyFile != filepath.Join(dir, "key") {
		t.Fatalf("relative key path not resolved: %+v, %v", loaded, err)
	}
	for _, content := range []string{string(data) + " {}", `{"unknown_setting":true}`} {
		if err := os.WriteFile(file, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadConfig(file); err == nil {
			t.Fatal("ambiguous or unknown configuration accepted")
		}
	}
}

// An in-process SSH relay exercises the actual SSH authentication, reverse
// forwarding requests and forwarded-tcpip channels, without remote machines.
type testRelay struct {
	listener net.Listener
	ready    chan string
	hostKey  ssh.Signer
	mu       sync.Mutex
	closers  []io.Closer
	workers  sync.WaitGroup
}

func newTestRelay(t *testing.T, authorized ssh.PublicKey) *testRelay {
	t.Helper()
	host, _ := testKey(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := &testRelay{listener: listener, ready: make(chan string, 8), hostKey: host}
	scfg := &ssh.ServerConfig{PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if meta.User() != "tunnel" || !bytes.Equal(key.Marshal(), authorized.Marshal()) {
			return nil, fmt.Errorf("unauthorized key")
		}
		return nil, nil
	}}
	scfg.AddHostKey(host)
	r.workers.Add(1)
	go func() {
		defer r.workers.Done()
		for {
			raw, err := listener.Accept()
			if err != nil {
				return
			}
			r.mu.Lock()
			r.closers = append(r.closers, raw)
			r.mu.Unlock()
			r.workers.Add(1)
			go func() {
				defer r.workers.Done()
				defer raw.Close()
				conn, channels, requests, err := ssh.NewServerConn(raw, scfg)
				if err != nil {
					return
				}
				defer conn.Close()
				go func() {
					for ch := range channels {
						ch.Reject(ssh.Prohibited, "relay accepts reverse forwarding only")
					}
				}()
				var forwarded net.Listener
				defer func() {
					if forwarded != nil {
						forwarded.Close()
					}
				}()
				for req := range requests {
					switch req.Type {
					case "tcpip-forward":
						var bind struct {
							Address string
							Port    uint32
						}
						if ssh.Unmarshal(req.Payload, &bind) != nil || bind.Address != "127.0.0.1" {
							req.Reply(false, nil)
							continue
						}
						forwarded, err = net.Listen("tcp", net.JoinHostPort(bind.Address, strconv.Itoa(int(bind.Port))))
						if err != nil {
							req.Reply(false, nil)
							continue
						}
						port := uint32(forwarded.Addr().(*net.TCPAddr).Port)
						req.Reply(true, ssh.Marshal(struct{ Port uint32 }{port}))
						r.ready <- forwarded.Addr().String()
						r.workers.Add(1)
						go r.forward(conn, forwarded)
					case "cancel-tcpip-forward":
						if forwarded != nil {
							forwarded.Close()
						}
						req.Reply(true, nil)
					default:
						req.Reply(true, nil)
					}
				}
			}()
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		r.mu.Lock()
		for _, c := range r.closers {
			c.Close()
		}
		r.mu.Unlock()
		done := make(chan struct{})
		go func() { r.workers.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("test relay did not shut down")
		}
	})
	return r
}

func (r *testRelay) forward(conn *ssh.ServerConn, listener net.Listener) {
	defer r.workers.Done()
	for {
		network, err := listener.Accept()
		if err != nil {
			return
		}
		r.workers.Add(1)
		go func() {
			defer r.workers.Done()
			defer network.Close()
			dest := listener.Addr().(*net.TCPAddr)
			origin := network.RemoteAddr().(*net.TCPAddr)
			payload := ssh.Marshal(struct {
				Address       string
				Port          uint32
				OriginAddress string
				OriginPort    uint32
			}{dest.IP.String(), uint32(dest.Port), origin.IP.String(), uint32(origin.Port)})
			channel, requests, err := conn.OpenChannel("forwarded-tcpip", payload)
			if err != nil {
				return
			}
			defer channel.Close()
			go ssh.DiscardRequests(requests)
			uploadDone := make(chan struct{})
			go func() {
				io.Copy(channel, network)
				channel.CloseWrite()
				close(uploadDone)
			}()
			io.Copy(network, channel)
			network.(*net.TCPConn).CloseWrite()
			network.Close()
			<-uploadDone
		}()
	}
}

func (r *testRelay) waitReady(t *testing.T) string {
	t.Helper()
	select {
	case address := <-r.ready:
		return address
	case <-time.After(8 * time.Second):
		t.Fatal("reverse port was not registered")
		return ""
	}
}

func relayConfig(t *testing.T, relay *testRelay, private []byte) (config, *ssh.ClientConfig) {
	t.Helper()
	keyFile := filepath.Join(t.TempDir(), "relay-key")
	if err := os.WriteFile(keyFile, private, 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config{
		RelayAddress: relay.listener.Addr().String(), RelayUser: "tunnel", PrivateKeyFile: keyFile,
		RelayHostKeySHA256: ssh.FingerprintSHA256(relay.hostKey.PublicKey()),
		// Only the test harness uses an automatically assigned port.
		RemoteListen: "127.0.0.1:0",
	}
	auth, err := cfg.sshConfig()
	if err != nil {
		t.Fatal(err)
	}
	return cfg, auth
}

func TestReverseTunnelForwardsBinaryAndHalfCloseAndReconnects(t *testing.T) {
	signer, private := testKey(t)
	relay := newTestRelay(t, signer.PublicKey())
	cfg, auth := relayConfig(t, relay, private)
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	cfg.TargetAddress = target.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for {
			conn, err := target.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(5 * time.Second))
				body, err := io.ReadAll(conn)
				if err == nil {
					// A response after request EOF catches premature full closes.
					conn.Write(append([]byte("response:"), body...))
				}
			}()
		}
	}()
	done := make(chan struct{})
	go func() {
		run(ctx, cfg, auth, log.New(io.Discard, "", 0), serve)
		close(done)
	}()
	address := relay.waitReady(t)
	exchange := func(address string, body []byte) error {
		conn, err := net.DialTimeout("tcp", address, time.Second)
		if err != nil {
			return err
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := conn.Write(body); err != nil {
			return err
		}
		conn.(*net.TCPConn).CloseWrite()
		response, err := io.ReadAll(conn)
		if err != nil {
			return err
		}
		if !bytes.Equal(response, append([]byte("response:"), body...)) {
			return fmt.Errorf("forwarded response corrupted or truncated: got %d bytes", len(response))
		}
		return nil
	}
	errors := make(chan error, 2)
	for _, payload := range [][]byte{[]byte("SSH-2.0-test\r\n"), bytes.Repeat([]byte{0, 1, 255, 128}, 65536)} {
		go func() { errors <- exchange(address, payload) }()
	}
	for range 2 {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
	// Interrupt the underlying SSH transport, then verify data after reconnect.
	relay.mu.Lock()
	for _, c := range relay.closers {
		c.Close()
	}
	relay.mu.Unlock()
	address = relay.waitReady(t)
	if err := exchange(address, []byte("after reconnect")); err != nil {
		t.Fatal(err)
	}
	// An idle forwarded connection must not prevent cancellation/shutdown.
	idle, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer idle.Close()
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("tunnel did not close active connections on cancellation")
	}
}

func TestRelayHostKeyMismatchIsRejected(t *testing.T) {
	signer, private := testKey(t)
	relay := newTestRelay(t, signer.PublicKey())
	cfg, _ := relayConfig(t, relay, private)
	wrong, _ := testKey(t)
	cfg.RelayHostKeySHA256 = ssh.FingerprintSHA256(wrong.PublicKey())
	auth, err := cfg.sshConfig()
	if err != nil {
		t.Fatal(err)
	}
	client, err := connectSSH(context.Background(), cfg.RelayAddress, auth)
	if client != nil {
		client.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "host key mismatch") {
		t.Fatalf("unexpected host key accepted: %v", err)
	}
}
