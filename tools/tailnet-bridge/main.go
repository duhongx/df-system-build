package main

import (
	"bufio"
	"context"
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
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"tailscale.com/tsnet"
)

type config struct {
	Hostname       string `json:"hostname"`
	TargetAddress  string `json:"target_address"`
	ListenPort     int    `json:"listen_port"`
	StateDirectory string `json:"state_directory"`
}

func main() {
	if err := execute(); err != nil {
		log.Print(err)
		// Keep an Explorer-launched console visible so a startup error doesn't
		// disappear immediately. Piped/non-interactive invocations never wait.
		if runtime.GOOS == "windows" {
			if info, statErr := os.Stdin.Stat(); statErr == nil && info.Mode()&os.ModeCharDevice != 0 {
				fmt.Fprintln(os.Stderr, "Press Enter to close this window.")
				_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
			}
		}
		os.Exit(1)
	}
}

func execute() error {
	path := flag.String("config", "", "optional JSON configuration file; default uses the built-in 10.212.11.18:22 profile")
	check := flag.Bool("check", false, "check target TCP reachability without joining Tailscale")
	printConfig := flag.Bool("print-config", false, "print effective configuration without connecting or creating login state")
	licenses := flag.Bool("licenses", false, "print bundled third-party license notices")
	flag.Parse()
	if flag.NArg() != 0 {
		return errors.New("unexpected arguments; run without arguments or use -config bridge.json")
	}
	if *licenses {
		_, err := fmt.Fprint(os.Stdout, thirdPartyLicenses)
		return err
	}
	cfg, err := resolveConfig(*path)
	if err != nil {
		return err
	}
	if *printConfig {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(cfg)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if *check {
		conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", cfg.TargetAddress)
		if err != nil {
			return fmt.Errorf("target %s is not reachable: %w", cfg.TargetAddress, err)
		}
		conn.Close()
		log.Printf("target TCP port is reachable: %s (SSH authentication has not been checked)", cfg.TargetAddress)
		return nil
	}
	return run(ctx, cfg, log.Default())
}

func resolveConfig(path string) (config, error) {
	if path != "" {
		return loadConfig(path)
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return config{}, fmt.Errorf("locate login state directory: %w", err)
	}
	return config{
		Hostname:       "windows-ssh-bridge",
		TargetAddress:  "10.212.11.18:22",
		ListenPort:     22022,
		StateDirectory: filepath.Join(base, "df-tailnet-bridge", "state"),
	}, nil
}

func loadConfig(path string) (config, error) {
	cfg := config{Hostname: "windows-ssh-bridge", ListenPort: 22022, StateDirectory: "state"}
	f, err := os.Open(path)
	if err != nil {
		return cfg, fmt.Errorf("open configuration: %w", err)
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || info.Size() > 64*1024 {
		return cfg, errors.New("configuration cannot be read or exceeds 64 KiB")
	}
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("parse configuration: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return cfg, errors.New("configuration must contain exactly one JSON object")
	}
	if !regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`).MatchString(cfg.Hostname) {
		return cfg, errors.New("hostname must be a DNS label, such as windows-ssh-bridge")
	}
	host, port, err := net.SplitHostPort(cfg.TargetAddress)
	if err != nil || host == "" || strings.ContainsAny(host, " \t\r\n") {
		return cfg, errors.New("target_address must be the server address reachable from Windows, such as 192.168.1.150:22")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 || cfg.ListenPort < 1 || cfg.ListenPort > 65535 {
		return cfg, errors.New("target port and listen_port must be between 1 and 65535")
	}
	if strings.TrimSpace(cfg.StateDirectory) == "" {
		return cfg, errors.New("state_directory cannot be empty")
	}
	if !filepath.IsAbs(cfg.StateDirectory) {
		base, err := filepath.Abs(filepath.Dir(path))
		if err != nil {
			return cfg, err
		}
		cfg.StateDirectory = filepath.Join(base, cfg.StateDirectory)
	}
	return cfg, nil
}

func run(ctx context.Context, cfg config, logger *log.Logger) error {
	if err := os.MkdirAll(cfg.StateDirectory, 0700); err != nil {
		return fmt.Errorf("create node state directory: %w", err)
	}
	// This node exists only inside this process. It installs no system service,
	// TUN driver or OS routes, and never shells out to ssh.exe or tailscale.exe.
	srv := &tsnet.Server{
		Dir: cfg.StateDirectory, Hostname: cfg.Hostname,
		UserLogf: logger.Printf,
		Logf:     func(string, ...any) {},
	}
	defer srv.Close()
	logger.Printf("starting private bridge %s -> %s", cfg.Hostname, cfg.TargetAddress)
	logger.Print("first run: open the authentication URL below and sign in to the same Tailscale network as your Mac")
	status, err := srv.Up(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("join Tailscale: %w", err)
	}
	listener, err := srv.Listen("tcp", net.JoinHostPort("", strconv.Itoa(cfg.ListenPort)))
	if err != nil {
		return fmt.Errorf("listen within Tailscale: %w", err)
	}
	defer listener.Close()
	logger.Print("bridge ready; keep this window open while using it")
	for _, ip := range status.TailscaleIPs {
		if ip.Is4() {
			logger.Printf("on Mac: ssh -p %d SERVER_USER@%s", cfg.ListenPort, ip)
		}
	}
	logger.Printf("port %d is available only inside your Tailscale network; access follows its network policy", cfg.ListenPort)
	return serveConnections(ctx, listener, cfg.TargetAddress, logger)
}

func serveConnections(ctx context.Context, listener net.Listener, target string, logger *log.Logger) error {
	ctx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	stop := context.AfterFunc(ctx, func() { _ = listener.Close() })
	defer func() {
		cancel()
		listener.Close()
		stop()
		workers.Wait()
	}()
	limit := make(chan struct{}, 16)
	for {
		incoming, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("tailnet listener: %w", err)
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
			outgoing, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", target)
			if err != nil {
				logger.Printf("connect target %s: %v", target, err)
				return
			}
			defer outgoing.Close()
			bridge(ctx, incoming, outgoing)
		}()
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
