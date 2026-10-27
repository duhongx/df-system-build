package main

import (
	"bytes"
	"context"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestBuiltInProfileIgnoresFilesInWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	// An old extracted configuration must not silently override the target
	// baked into the double-click executable.
	if err := os.WriteFile("bridge.json", []byte(`{"target_address":"192.168.1.150:22"}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := resolveConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TargetAddress != "10.212.11.18:22" || cfg.ListenPort != 22022 || !filepath.IsAbs(cfg.StateDirectory) {
		t.Fatalf("incorrect built-in profile: %+v", cfg)
	}
	if _, err := os.Stat(filepath.Join(dir, "state")); !os.IsNotExist(err) {
		t.Fatalf("resolving configuration created login state in the working directory: %v", err)
	}
}

func TestConfigurationNeedsOnlyTargetAndResolvesStateDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bridge.json")
	if err := os.WriteFile(path, []byte(`{"target_address":"192.168.1.150:22"}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenPort != 22022 || cfg.Hostname != "windows-ssh-bridge" || cfg.StateDirectory != filepath.Join(dir, "state") {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	for _, body := range []string{
		`{"target_address":"192.168.1.150"}`,
		`{"target_address":"192.168.1.150:70000"}`,
		`{"target_address":"192.168.1.150:22","listen_port":0}`,
		`{"target_address":"192.168.1.150:22","hostname":"bad hostname"}`,
		`{"target_address":"192.168.1.150:22","state_directory":""}`,
		`{"target_address":"192.168.1.150:22","relay_address":"old-config"}`,
		`{"target_address":"192.168.1.150:22"} {}`,
	} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadConfig(path); err == nil {
			t.Fatalf("invalid configuration accepted: %s", body)
		}
	}
}

func listen(t *testing.T) net.Listener {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

func echoAfterEOF(t *testing.T, target net.Listener) {
	t.Helper()
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			conn, err := target.Accept()
			if err != nil {
				return
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(5 * time.Second))
				body, err := io.ReadAll(conn)
				if err == nil {
					conn.Write(append([]byte("reply:"), body...))
				}
			}()
		}
	}()
	t.Cleanup(func() {
		target.Close()
		workers.Wait()
	})
}

func exchange(address string, payload []byte) ([]byte, error) {
	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write(payload); err != nil {
		return nil, err
	}
	conn.(*net.TCPConn).CloseWrite()
	return io.ReadAll(conn)
}

func TestForwardingPreservesConcurrentBinaryStreamsAndHalfClose(t *testing.T) {
	target := listen(t)
	echoAfterEOF(t, target)
	front := listen(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serveConnections(ctx, front, target.Addr().String(), log.New(io.Discard, "", 0)) }()
	results := make(chan error, 2)
	for _, payload := range [][]byte{[]byte("SSH-2.0-test\r\n"), bytes.Repeat([]byte{0, 255, 128, 1}, 65536)} {
		go func() {
			response, err := exchange(front.Addr().String(), payload)
			if err == nil && !bytes.Equal(response, append([]byte("reply:"), payload...)) {
				err = io.ErrUnexpectedEOF
			}
			results <- err
		}()
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	idle, err := net.DialTimeout("tcp", front.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer idle.Close()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not release active forwarding connections")
	}
}

func TestTargetFailureDoesNotTerminateBridge(t *testing.T) {
	reservation := listen(t)
	address := reservation.Addr().String()
	reservation.Close()
	front := listen(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serveConnections(ctx, front, address, log.New(io.Discard, "", 0)) }()
	conn, err := net.DialTimeout("tcp", front.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, err = conn.Read(make([]byte, 1))
	conn.Close()
	if err != io.EOF {
		t.Fatalf("unreachable target did not close its individual connection: %v", err)
	}
	select {
	case err := <-done:
		t.Fatalf("bridge stopped after a target connection failure: %v", err)
	default:
	}
	target, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	echoAfterEOF(t, target)
	response, err := exchange(front.Addr().String(), []byte("target recovered"))
	if err != nil || string(response) != "reply:target recovered" {
		t.Fatalf("target could not reconnect: %q, %v", response, err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("bridge did not stop")
	}
}
