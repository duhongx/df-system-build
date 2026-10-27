package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

const connectionTimeout = 10 * time.Second

type config struct {
	RelayAddress       string `json:"relay_address"`
	RelayUser          string `json:"relay_user"`
	PrivateKeyFile     string `json:"private_key_file"`
	KnownHostsFile     string `json:"known_hosts_file,omitempty"`
	RelayHostKeySHA256 string `json:"relay_host_key_sha256,omitempty"`
	TargetAddress      string `json:"target_address"`
	RemoteListen       string `json:"remote_listen_address"`
}

func main() {
	path := flag.String("config", "tunnel.json", "JSON configuration file")
	flag.Parse()
	if flag.NArg() != 0 {
		log.Fatal("unexpected arguments; use -config tunnel.json")
	}
	cfg, err := loadConfig(*path)
	if err != nil {
		log.Fatal(err)
	}
	clientConfig, err := cfg.sshConfig()
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	log.Printf("reverse SSH tunnel: relay=%s, listen=%s, target=%s", cfg.RelayAddress, cfg.RemoteListen, cfg.TargetAddress)
	run(ctx, cfg, clientConfig, log.Default(), serve)
	log.Print("tunnel stopped")
}

func loadConfig(path string) (config, error) {
	var cfg config
	f, err := os.Open(path)
	if err != nil {
		return cfg, fmt.Errorf("open configuration: %w", err)
	}
	defer f.Close()
	decoder := json.NewDecoder(io.LimitReader(f, 64*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("read configuration: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return cfg, errors.New("configuration must contain exactly one JSON object")
	}
	if err := cfg.validate(); err != nil {
		return cfg, err
	}
	base, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return cfg, err
	}
	for _, file := range []*string{&cfg.PrivateKeyFile, &cfg.KnownHostsFile} {
		if *file != "" && !filepath.IsAbs(*file) {
			*file = filepath.Join(base, *file)
		}
	}
	return cfg, nil
}

func validateAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil || strings.TrimSpace(host) == "" || strings.ContainsAny(host, " \t\r\n") {
		return fmt.Errorf("invalid host:port address %q", address)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("invalid port in %q", address)
	}
	return nil
}

func (c config) validate() error {
	for _, address := range []string{c.RelayAddress, c.TargetAddress, c.RemoteListen} {
		if err := validateAddress(address); err != nil {
			return err
		}
	}
	host, _, _ := net.SplitHostPort(c.RemoteListen)
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return errors.New("remote_listen_address must use a loopback IP such as 127.0.0.1:22022")
	}
	if strings.TrimSpace(c.RelayUser) == "" || c.PrivateKeyFile == "" {
		return errors.New("relay_user and private_key_file are required")
	}
	if (c.KnownHostsFile == "") == (c.RelayHostKeySHA256 == "") {
		return errors.New("set exactly one of known_hosts_file or relay_host_key_sha256")
	}
	if c.RelayHostKeySHA256 != "" {
		decoded, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(c.RelayHostKeySHA256, "SHA256:"))
		if !strings.HasPrefix(c.RelayHostKeySHA256, "SHA256:") || err != nil || len(decoded) != 32 {
			return errors.New("relay_host_key_sha256 must be a verified OpenSSH SHA256 fingerprint")
		}
	}
	return nil
}

func (c config) sshConfig() (*ssh.ClientConfig, error) {
	key, err := os.ReadFile(c.PrivateKeyFile)
	if err != nil {
		return nil, fmt.Errorf("read relay private key: %w", err)
	}
	var signer ssh.Signer
	if passphrase := os.Getenv("SSH_TUNNEL_KEY_PASSPHRASE"); passphrase != "" {
		signer, err = ssh.ParsePrivateKeyWithPassphrase(key, []byte(passphrase))
	} else {
		signer, err = ssh.ParsePrivateKey(key)
	}
	if err != nil {
		return nil, fmt.Errorf("parse relay private key (encrypted keys use SSH_TUNNEL_KEY_PASSPHRASE): %w", err)
	}
	var hostKey ssh.HostKeyCallback
	if c.RelayHostKeySHA256 != "" {
		hostKey = func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if actual := ssh.FingerprintSHA256(key); actual != c.RelayHostKeySHA256 {
				return fmt.Errorf("relay host key mismatch: got %s", actual)
			}
			return nil
		}
	} else {
		hostKey, err = knownhosts.New(c.KnownHostsFile)
		if err != nil {
			return nil, fmt.Errorf("read relay known_hosts: %w", err)
		}
	}
	return &ssh.ClientConfig{User: c.RelayUser, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: hostKey}, nil
}

type sessionFunc func(context.Context, config, *ssh.ClientConfig, *log.Logger) error

func run(ctx context.Context, cfg config, auth *ssh.ClientConfig, logger *log.Logger, session sessionFunc) {
	backoff := 2 * time.Second
	for ctx.Err() == nil {
		started := time.Now()
		err := session(ctx, cfg, auth, logger)
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) >= time.Minute {
			backoff = 2 * time.Second
		}
		logger.Printf("tunnel disconnected: %v; reconnecting in %s", err, backoff)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

func connectSSH(ctx context.Context, address string, auth *ssh.ClientConfig) (*ssh.Client, error) {
	raw, err := (&net.Dialer{Timeout: connectionTimeout}).DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("connect relay: %w", err)
	}
	stop := context.AfterFunc(ctx, func() { _ = raw.Close() })
	defer stop()
	if err := raw.SetDeadline(time.Now().Add(connectionTimeout)); err != nil {
		raw.Close()
		return nil, err
	}
	conn, channels, requests, err := ssh.NewClientConn(raw, address, auth)
	if err != nil {
		raw.Close()
		return nil, fmt.Errorf("authenticate relay: %w", err)
	}
	if err := raw.SetDeadline(time.Time{}); err != nil {
		conn.Close()
		return nil, err
	}
	return ssh.NewClient(conn, channels, requests), nil
}

func serve(ctx context.Context, cfg config, auth *ssh.ClientConfig, logger *log.Logger) error {
	client, err := connectSSH(ctx, cfg.RelayAddress, auth)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(ctx, func() { _ = client.Close() })
	var workers sync.WaitGroup
	defer func() {
		cancel()
		client.Close()
		stop()
		workers.Wait()
	}()
	setupTimer := time.AfterFunc(connectionTimeout, func() { _ = client.Close() })
	listener, err := client.Listen("tcp", cfg.RemoteListen)
	setupTimer.Stop()
	if err != nil {
		return fmt.Errorf("open reverse port (check relay forwarding permission and port availability): %w", err)
	}
	defer listener.Close()
	workers.Add(1)
	go func() {
		defer workers.Done()
		keepAlive(ctx, client)
	}()
	logger.Printf("tunnel ready: %s on relay -> %s through this machine", listener.Addr(), cfg.TargetAddress)
	limit := make(chan struct{}, 16)
	for {
		incoming, err := listener.Accept()
		if err != nil {
			return fmt.Errorf("reverse listener closed: %w", err)
		}
		select {
		case limit <- struct{}{}:
		default:
			incoming.Close()
			logger.Print("connection rejected: 16 active connections already")
			continue
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer func() { <-limit }()
			defer incoming.Close()
			target, err := (&net.Dialer{Timeout: connectionTimeout}).DialContext(ctx, "tcp", cfg.TargetAddress)
			if err != nil {
				logger.Printf("connect target %s: %v", cfg.TargetAddress, err)
				return
			}
			defer target.Close()
			bridge(ctx, incoming, target)
		}()
	}
}

func keepAlive(ctx context.Context, client *ssh.Client) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		// Closing the SSH connection also releases a blocked SendRequest.
		timeout := time.AfterFunc(connectionTimeout, func() { _ = client.Close() })
		_, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
		timeout.Stop()
		if err != nil {
			client.Close()
			return
		}
	}
}

func bridge(ctx context.Context, a, b net.Conn) {
	closeBoth := func() { a.Close(); b.Close() }
	stop := context.AfterFunc(ctx, closeBoth)
	defer stop()
	defer closeBoth()
	done := make(chan error, 2)
	copyOne := func(dst, src net.Conn) {
		_, err := io.Copy(dst, src)
		if half, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = half.CloseWrite()
		} else {
			dst.Close()
		}
		done <- err
	}
	go copyOne(a, b)
	go copyOne(b, a)
	if err := <-done; err != nil {
		closeBoth()
	}
	<-done
}
