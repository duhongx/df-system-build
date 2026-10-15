package service

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"net"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"df-build-server/internal/model"
	"df-build-server/pkg/crypto"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

func TestLogCollectionRealSFTPAndCancellation(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	serverConfig := &ssh.ServerConfig{PasswordCallback: func(meta ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
		if meta.User() == "tester" && string(password) == "test-password" {
			return nil, nil
		}
		return nil, errors.New("incorrect password")
	}}
	serverConfig.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var clients sync.WaitGroup
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			clients.Add(1)
			go func() {
				defer clients.Done()
				defer conn.Close()
				_, channels, requests, err := ssh.NewServerConn(conn, serverConfig)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(requests)
				for channel := range channels {
					if channel.ChannelType() != "session" {
						_ = channel.Reject(ssh.UnknownChannelType, "session only")
						continue
					}
					session, requests, err := channel.Accept()
					if err != nil {
						return
					}
					go func() {
						for request := range requests {
							_ = request.Reply(request.Type == "subsystem", nil)
						}
					}()
					server, err := sftp.NewServer(session)
					if err != nil {
						return
					}
					_ = server.Serve()
					_ = server.Close()
				}
			}()
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); <-acceptDone; clients.Wait() })
	_, portString, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portString)
	password, err := crypto.Encrypt("test-password")
	if err != nil {
		t.Fatal(err)
	}
	host := model.Server{Host: "127.0.0.1", Port: port, Username: "tester", AuthType: "password", CredentialEncrypted: password, ConnTimeout: 2}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remote, err := connectLogRemote(ctx, host)
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	root := t.TempDir()
	logTestWrite(t, filepath.Join(root, "rabbit.log"), "real sftp content")
	source := model.LogSource{Directory: root, Pattern: "*.log"}
	files, err := listLogFiles(ctx, remote, source)
	if err != nil || len(files) != 1 {
		t.Fatalf("%+v %v", files, err)
	}
	remote.(*logSFTP).ioTimeout = 100 * time.Millisecond
	if _, err := remote.RealPath(root); err != nil {
		t.Fatal(err)
	}
	// Local compression must not expire the idle SSH background reader.
	time.Sleep(200 * time.Millisecond)
	if _, err := remote.RealPath(root); err != nil {
		t.Fatalf("idle connection expired: %v", err)
	}
	job := logTestJob(root, "rabbit.log")
	dir := t.TempDir()
	if err := collectLogArchive(ctx, job, dir, func(model.LogSource) (logRemote, error) { return connectLogRemote(ctx, host) }, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if files := logTestZIP(t, dir); files[job.Items[0].ArchivePath] != "real sftp content" {
		t.Fatalf("%+v", files)
	}
	cancel()
	if _, err := remote.RealPath(root); err == nil { // Let the cancellation callback close the transport before asserting.
		_ = remote.Close()
		if _, err := remote.RealPath(root); err == nil {
			t.Fatal("SFTP transport still usable after close")
		}
	}
	host.CredentialEncrypted, _ = crypto.Encrypt("incorrect")
	if bad, err := connectLogRemote(context.Background(), host); err == nil {
		bad.Close()
		t.Fatal("incorrect SSH password accepted")
	}
}
