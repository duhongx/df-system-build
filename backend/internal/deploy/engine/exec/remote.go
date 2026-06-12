package exec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// SSHConfig describes how to reach a single target host. Builds an
// ssh.ClientConfig under the hood; callers don't need to know the
// underlying library to use RemoteBackend.
type SSHConfig struct {
	Addr           string        // host:port; defaults port 22 if missing
	User           string        // ssh user
	PrivateKeyPath string        // path on the control node; mutually exclusive with PrivateKey
	PrivateKey     []byte        // PEM bytes; takes precedence over PrivateKeyPath
	Password       string        // optional, used as fallback or alongside agent
	HostKey        []byte        // optional pinned host key (authorized_keys format); empty = TOFU/insecure-ignore
	DialTimeout    time.Duration // default 15s
	KeepAlive      time.Duration // default 30s
}

// RemoteBackend is an SSH+SFTP implementation of Backend. One
// RemoteBackend instance owns one ssh.Client to the target host; both
// the Commander and Filesystem halves multiplex sessions / sftp
// channels over that single TCP connection.
//
// Construct via NewRemoteBackend. Always Close() before discarding —
// doing so releases the SFTP subsystem and the underlying TCP socket.
type RemoteBackend struct {
	cfg    SSHConfig
	client *ssh.Client

	sftpOnce sync.Once
	sftpErr  error
	sftp     *sftp.Client
}

// NewRemoteBackend dials the target host and returns a Backend whose
// FS and Cmd halves operate on it. The caller MUST call (*RemoteBackend).Close
// when done; the returned Backend's FS exposes the underlying
// *RemoteBackend via the Closer() helper.
//
// Authentication priority:
//  1. PrivateKey bytes (if set)
//  2. PrivateKeyPath (read at dial time)
//  3. Password (if set)
//
// Multiple methods stack; SSH negotiates the first the server accepts.
func NewRemoteBackend(cfg SSHConfig) (*RemoteBackend, Backend, error) {
	if cfg.Addr == "" {
		return nil, Backend{}, errors.New("RemoteBackend: Addr is required")
	}
	if cfg.User == "" {
		return nil, Backend{}, errors.New("RemoteBackend: User is required")
	}
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = 15 * time.Second
	}
	if cfg.KeepAlive == 0 {
		cfg.KeepAlive = 30 * time.Second
	}
	if !strings.Contains(cfg.Addr, ":") {
		cfg.Addr = cfg.Addr + ":22"
	}

	auths, err := buildAuthMethods(cfg)
	if err != nil {
		return nil, Backend{}, err
	}
	hostKeyCallback, err := buildHostKeyCallback(cfg.HostKey)
	if err != nil {
		return nil, Backend{}, err
	}

	clientCfg := &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            auths,
		HostKeyCallback: hostKeyCallback,
		Timeout:         cfg.DialTimeout,
	}

	conn, err := net.DialTimeout("tcp", cfg.Addr, cfg.DialTimeout)
	if err != nil {
		return nil, Backend{}, fmt.Errorf("ssh dial %s: %w", cfg.Addr, err)
	}
	if cfg.KeepAlive > 0 {
		if tc, ok := conn.(*net.TCPConn); ok {
			_ = tc.SetKeepAlive(true)
			_ = tc.SetKeepAlivePeriod(cfg.KeepAlive)
		}
	}
	c, ch, reqs, err := ssh.NewClientConn(conn, cfg.Addr, clientCfg)
	if err != nil {
		_ = conn.Close()
		return nil, Backend{}, fmt.Errorf("ssh handshake %s: %w", cfg.Addr, err)
	}
	client := ssh.NewClient(c, ch, reqs)

	rb := &RemoteBackend{cfg: cfg, client: client}
	be := Backend{
		FS:  remoteFS{rb: rb},
		Cmd: remoteCmd{rb: rb},
	}
	return rb, be, nil
}

// Close shuts down the SFTP subsystem (if started) and the underlying
// SSH connection. Safe to call more than once.
func (rb *RemoteBackend) Close() error {
	var firstErr error
	if rb.sftp != nil {
		if err := rb.sftp.Close(); err != nil {
			firstErr = err
		}
		rb.sftp = nil
	}
	if rb.client != nil {
		if err := rb.client.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		rb.client = nil
	}
	return firstErr
}

// sftpClient lazily starts the SFTP subsystem the first time a
// filesystem op is issued. Reuses the same client for subsequent
// ops — sftp.Client is safe for concurrent use.
func (rb *RemoteBackend) sftpClient() (*sftp.Client, error) {
	rb.sftpOnce.Do(func() {
		c, err := sftp.NewClient(rb.client)
		if err != nil {
			rb.sftpErr = fmt.Errorf("open sftp on %s: %w", rb.cfg.Addr, err)
			return
		}
		rb.sftp = c
	})
	return rb.sftp, rb.sftpErr
}

func buildAuthMethods(cfg SSHConfig) ([]ssh.AuthMethod, error) {
	var auths []ssh.AuthMethod
	keyBytes := cfg.PrivateKey
	if len(keyBytes) == 0 && cfg.PrivateKeyPath != "" {
		b, err := os.ReadFile(cfg.PrivateKeyPath)
		if err != nil {
			return nil, fmt.Errorf("read private key %s: %w", cfg.PrivateKeyPath, err)
		}
		keyBytes = b
	}
	if len(keyBytes) > 0 {
		signer, err := ssh.ParsePrivateKey(keyBytes)
		if err != nil {
			return nil, fmt.Errorf("parse private key: %w", err)
		}
		auths = append(auths, ssh.PublicKeys(signer))
	}
	if cfg.Password != "" {
		auths = append(auths, ssh.Password(cfg.Password))
	}
	if len(auths) == 0 {
		return nil, errors.New("no SSH auth method configured (need PrivateKey, PrivateKeyPath, or Password)")
	}
	return auths, nil
}

// buildHostKeyCallback returns a host-key callback. If hostKey is
// empty we fall back to ssh.InsecureIgnoreHostKey — fine for an
// internal deployment tool inside a controlled network, but the door
// is open to swap in a known_hosts-backed callback when the operator
// pins keys via the UI.
func buildHostKeyCallback(hostKey []byte) (ssh.HostKeyCallback, error) {
	if len(hostKey) == 0 {
		return ssh.InsecureIgnoreHostKey(), nil
	}
	pk, _, _, _, err := ssh.ParseAuthorizedKey(hostKey)
	if err != nil {
		return nil, fmt.Errorf("parse host key: %w", err)
	}
	return ssh.FixedHostKey(pk), nil
}

// ──────────────────────────────────────────────────────────────────
// Commander
// ──────────────────────────────────────────────────────────────────

type remoteCmd struct{ rb *RemoteBackend }

func (c remoteCmd) Run(name string, args ...string) Result {
	return c.RunWithOptions(RunOptions{}, name, args...)
}

func (c remoteCmd) RunWithStdin(stdin []byte, name string, args ...string) Result {
	return c.RunWithOptions(RunOptions{Stdin: stdin}, name, args...)
}

func (c remoteCmd) RunWithOptions(opts RunOptions, name string, args ...string) Result {
	session, err := c.rb.client.NewSession()
	if err != nil {
		return Result{ExitCode: -1, Err: fmt.Errorf("new ssh session: %w", err)}
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr
	if opts.Stdin != nil {
		session.Stdin = bytes.NewReader(opts.Stdin)
	}

	// Build a single shell command line. We never want the remote
	// shell to interpret operators (|, ;, &&) the caller didn't ask
	// for, so every arg is single-quoted via shellQuote.
	cmdline := buildRemoteCommandLine(opts.WorkDir, name, args)

	// Timeout via context: spawn a goroutine that signals the
	// session when the deadline elapses.
	ctx := opts.Context
	if ctx == nil {
		ctx = context.Background()
	}
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}
	done := make(chan error, 1)
	go func() {
		done <- session.Run(cmdline)
	}()

	select {
	case <-ctx.Done():
		// Best-effort interrupt. Some servers don't honour SIGTERM
		// over SSH; the session.Close() that fires when this method
		// returns will tear the channel down regardless.
		_ = session.Signal(ssh.SIGTERM)
		return Result{
			Stdout:   stdout.Bytes(),
			Stderr:   stderr.Bytes(),
			ExitCode: -1,
			Err:      ctx.Err(),
		}
	case err := <-done:
		exit := 0
		if err != nil {
			var ee *ssh.ExitError
			if errors.As(err, &ee) {
				exit = ee.ExitStatus()
				err = nil
			} else {
				exit = -1
			}
		}
		return Result{
			Stdout:   stdout.Bytes(),
			Stderr:   stderr.Bytes(),
			ExitCode: exit,
			Err:      err,
		}
	}
}

// buildRemoteCommandLine joins the program name + args into a single
// quoted shell line, optionally cd-ing into a workdir first. We send
// it via session.Run() rather than passing argv directly because the
// SSH "exec" channel always runs through the user's login shell on
// the remote side anyway.
func buildRemoteCommandLine(workDir, name string, args []string) string {
	parts := []string{shellQuote(name)}
	for _, a := range args {
		parts = append(parts, shellQuote(a))
	}
	cmd := strings.Join(parts, " ")
	if workDir != "" {
		return fmt.Sprintf("cd %s && %s", shellQuote(workDir), cmd)
	}
	return cmd
}

// shellQuote wraps s in single quotes, escaping any single quotes
// inside. Identical to the helper in the dfctlv2 package, duplicated
// here to keep this file independent.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ──────────────────────────────────────────────────────────────────
// Filesystem
// ──────────────────────────────────────────────────────────────────

type remoteFS struct{ rb *RemoteBackend }

func (f remoteFS) Stat(path string) (os.FileInfo, error) {
	c, err := f.rb.sftpClient()
	if err != nil {
		return nil, err
	}
	return c.Stat(path)
}

func (f remoteFS) MkdirAll(path string, perm os.FileMode) error {
	c, err := f.rb.sftpClient()
	if err != nil {
		return err
	}
	if err := c.MkdirAll(path); err != nil {
		return err
	}
	// MkdirAll in pkg/sftp doesn't take perm; apply afterwards on
	// the leaf to keep semantics close to os.MkdirAll. Parent dirs
	// keep whatever perm they had — same as the stdlib behaviour
	// when intermediate dirs already existed.
	return c.Chmod(path, perm)
}

func (f remoteFS) WriteFile(path string, data []byte, perm os.FileMode) error {
	c, err := f.rb.sftpClient()
	if err != nil {
		return err
	}
	if err := c.MkdirAll(filepath.Dir(path)); err != nil {
		return err
	}
	tmp := path + ".dfctl-tmp"
	out, err := c.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC)
	if err != nil {
		return err
	}
	if _, err := out.Write(data); err != nil {
		_ = out.Close()
		_ = c.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = c.Remove(tmp)
		return err
	}
	if err := c.Chmod(tmp, perm); err != nil {
		_ = c.Remove(tmp)
		return err
	}
	// PosixRename atomically replaces dst on Linux. Some servers
	// don't advertise the extension; fall back to plain Rename
	// (which on most servers does ENOENT-then-rename).
	if err := c.PosixRename(tmp, path); err != nil {
		_ = c.Remove(path)
		if err2 := c.Rename(tmp, path); err2 != nil {
			_ = c.Remove(tmp)
			return err2
		}
	}
	return nil
}

func (f remoteFS) ReadFile(path string) ([]byte, error) {
	c, err := f.rb.sftpClient()
	if err != nil {
		return nil, err
	}
	in, err := c.Open(path)
	if err != nil {
		return nil, err
	}
	defer in.Close()
	return io.ReadAll(in)
}

func (f remoteFS) Remove(path string) error {
	c, err := f.rb.sftpClient()
	if err != nil {
		return err
	}
	if err := c.Remove(path); err != nil {
		// Match os.Remove's contract: callers using os.IsNotExist
		// expect the underlying error to wrap fs.ErrNotExist.
		if errors.Is(err, os.ErrNotExist) || strings.Contains(err.Error(), "does not exist") {
			return os.ErrNotExist
		}
		return err
	}
	return nil
}

func (f remoteFS) RemoveAll(path string) error {
	c, err := f.rb.sftpClient()
	if err != nil {
		return err
	}
	if err := c.RemoveAll(path); err != nil {
		// pkg/sftp surfaces a plain "file does not exist" when the
		// root path is missing. os.RemoveAll silently returns nil in
		// that case — make the two backends match so idempotent
		// rollback steps don't false-fail when the path was already
		// cleaned up.
		if errors.Is(err, os.ErrNotExist) || strings.Contains(err.Error(), "does not exist") {
			return nil
		}
		return err
	}
	return nil
}

func (f remoteFS) Chmod(path string, mode os.FileMode) error {
	c, err := f.rb.sftpClient()
	if err != nil {
		return err
	}
	return c.Chmod(path, mode)
}

func (f remoteFS) Chown(path string, uid, gid int) error {
	c, err := f.rb.sftpClient()
	if err != nil {
		return err
	}
	return c.Chown(path, uid, gid)
}

func (f remoteFS) Symlink(oldname, newname string) error {
	c, err := f.rb.sftpClient()
	if err != nil {
		return err
	}
	return c.Symlink(oldname, newname)
}

// PutFile streams a control-side file onto the remote host with
// write-then-rename semantics. Skips the upload entirely when the
// remote already has identical content (size + sha256), which saves
// real bandwidth on rerolls of large rpm/tar payloads.
func (f remoteFS) PutFile(srcLocal, dst string, mode os.FileMode) error {
	c, err := f.rb.sftpClient()
	if err != nil {
		return err
	}
	srcInfo, err := os.Stat(srcLocal)
	if err != nil {
		return err
	}

	// Quick equality check: same size + same sha256 → no-op + chmod.
	if equal, err := remoteFileMatchesLocal(c, srcLocal, dst, srcInfo); err == nil && equal {
		return c.Chmod(dst, mode)
	}

	if err := c.MkdirAll(filepath.Dir(dst)); err != nil {
		return err
	}
	tmp := dst + ".dfctl-tmp"
	in, err := os.Open(srcLocal)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := c.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = c.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = c.Remove(tmp)
		return err
	}
	if err := c.Chmod(tmp, mode); err != nil {
		_ = c.Remove(tmp)
		return err
	}
	if err := c.PosixRename(tmp, dst); err != nil {
		_ = c.Remove(dst)
		if err2 := c.Rename(tmp, dst); err2 != nil {
			_ = c.Remove(tmp)
			return err2
		}
	}
	return nil
}

// PutDir mirrors LocalBackend.PutDir but uses sftp underneath. Keeps
// the sftp client alive for the whole walk so we don't pay an extra
// channel-open per file.
func (f remoteFS) PutDir(srcLocal, dst string, fileMode os.FileMode) error {
	c, err := f.rb.sftpClient()
	if err != nil {
		return err
	}
	srcInfo, err := os.Stat(srcLocal)
	if err != nil {
		return err
	}
	if !srcInfo.IsDir() {
		return fmt.Errorf("PutDir: source is not a directory: %s", srcLocal)
	}
	if err := c.MkdirAll(dst); err != nil {
		return err
	}
	return filepath.Walk(srcLocal, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(srcLocal, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case info.IsDir():
			if err := c.MkdirAll(target); err != nil {
				return err
			}
			return c.Chmod(target, info.Mode().Perm())
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			_ = c.Remove(target)
			return c.Symlink(link, target)
		default:
			perm := info.Mode().Perm()
			if fileMode != 0 {
				perm = fileMode
			}
			return f.PutFile(path, target, perm)
		}
	})
}

// remoteFileMatchesLocal returns true if dst on the remote and src on
// the local FS have the same size and content. Reads dst over the
// wire — only call after verifying both sides are regular files.
//
// We reuse the helper from local.go indirectly: the local sha is
// streamed; the remote sha is computed via an SFTP read. Worst case
// we transfer the file twice (once to compare, once to upload) — but
// since this only fires when sizes match, the practical cost is "we
// re-read on no-op replays of huge tarballs".
func remoteFileMatchesLocal(c *sftp.Client, srcLocal, dst string, srcInfo os.FileInfo) (bool, error) {
	dstInfo, err := c.Stat(dst)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if !srcInfo.Mode().IsRegular() || !dstInfo.Mode().IsRegular() {
		return false, nil
	}
	if srcInfo.Size() != dstInfo.Size() {
		return false, nil
	}

	srcF, err := os.Open(srcLocal)
	if err != nil {
		return false, err
	}
	defer srcF.Close()
	dstF, err := c.Open(dst)
	if err != nil {
		return false, err
	}
	defer dstF.Close()

	bufA := make([]byte, 64*1024)
	bufB := make([]byte, 64*1024)
	for {
		nA, errA := srcF.Read(bufA)
		nB, errB := io.ReadFull(dstF, bufB[:nA])
		if errB == io.EOF || errB == io.ErrUnexpectedEOF {
			if nA != nB {
				return false, nil
			}
		} else if errB != nil && errB != io.EOF {
			return false, errB
		}
		if !bytes.Equal(bufA[:nA], bufB[:nB]) {
			return false, nil
		}
		if errA == io.EOF {
			return true, nil
		}
		if errA != nil {
			return false, errA
		}
	}
}
