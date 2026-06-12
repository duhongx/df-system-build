package exec

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	osexec "os/exec"
	"path/filepath"
)

// LocalBackend is the os.* / os/exec implementation of Backend. It
// targets the local machine the dfctl-web process runs on — used for
// tests, dev, and as a real backend when a host's address resolves to
// the deploy node itself.
//
// Construct with NewLocalBackend(); zero value is not usable.
type LocalBackend struct{}

// NewLocalBackend returns a Backend wired to the local filesystem
// and process.
func NewLocalBackend() Backend {
	b := &LocalBackend{}
	return Backend{FS: localFS{b}, Cmd: localCmd{b}}
}

// localFS is the Filesystem half of LocalBackend. Kept as a separate
// unexported type so the public surface is just NewLocalBackend.
type localFS struct{ *LocalBackend }

func (localFS) Stat(path string) (os.FileInfo, error)        { return os.Stat(path) }
func (localFS) MkdirAll(path string, perm os.FileMode) error { return os.MkdirAll(path, perm) }
func (localFS) WriteFile(path string, data []byte, perm os.FileMode) error {
	return os.WriteFile(path, data, perm)
}
func (localFS) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }
func (localFS) Remove(path string) error             { return os.Remove(path) }
func (localFS) RemoveAll(path string) error          { return os.RemoveAll(path) }
func (localFS) Chmod(path string, mode os.FileMode) error {
	return os.Chmod(path, mode)
}
func (localFS) Chown(path string, uid, gid int) error {
	return os.Chown(path, uid, gid)
}
func (localFS) Symlink(oldname, newname string) error {
	return os.Symlink(oldname, newname)
}

// PutFile copies a local file onto itself (same machine), preserving
// stream semantics so the implementation matches what RemoteBackend
// will eventually do over SFTP.
//
// Mirrors the legacy copyRegularFile behaviour: if dst already has
// identical content (and same size, regular file), the file is left
// untouched — preserves mtime so downstream `creates`-based
// idempotency checks still work.
func (localFS) PutFile(srcLocal, dst string, mode os.FileMode) error {
	if same, err := regularFileContentEqual(srcLocal, dst); err != nil {
		return err
	} else if same {
		// Source and dst already byte-equal. Make sure mode matches
		// before bailing — `cp -p` semantics.
		return os.Chmod(dst, mode)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(srcLocal)
	if err != nil {
		return err
	}
	defer in.Close()

	// Use a temp file so a partial copy never appears at dst —
	// matches RemoteBackend's intended write-then-rename behaviour.
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".dfctl-put-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, dst)
}

// regularFileContentEqual returns true if both paths exist as
// regular files of the same size and identical byte content.
// Returns false (no error) if dst does not exist — that's the common
// "first-time copy" case.
func regularFileContentEqual(src, dst string) (bool, error) {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return false, err
	}
	dstInfo, err := os.Stat(dst)
	if err != nil {
		if os.IsNotExist(err) {
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
	a, err := os.Open(src)
	if err != nil {
		return false, err
	}
	defer a.Close()
	b, err := os.Open(dst)
	if err != nil {
		return false, err
	}
	defer b.Close()
	bufA := make([]byte, 32*1024)
	bufB := make([]byte, 32*1024)
	for {
		nA, errA := a.Read(bufA)
		nB, errB := b.Read(bufB)
		if nA != nB || !bytesEqualPrefix(bufA, bufB, nA) {
			return false, nil
		}
		if errA == io.EOF && errB == io.EOF {
			return true, nil
		}
		if errA != nil && errA != io.EOF {
			return false, errA
		}
		if errB != nil && errB != io.EOF {
			return false, errB
		}
	}
}

func bytesEqualPrefix(a, b []byte, n int) bool {
	if n > len(a) || n > len(b) {
		return false
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// PutDir copies a directory tree using filepath.WalkDir. Behaviour
// matches `cp -a` for directories and symlinks. Regular files use
// fileMode if non-zero, otherwise they keep their source mode.
func (b localFS) PutDir(srcLocal, dst string, fileMode os.FileMode) error {
	srcInfo, err := os.Stat(srcLocal)
	if err != nil {
		return err
	}
	if !srcInfo.IsDir() {
		return errors.New("PutDir: source is not a directory: " + srcLocal)
	}
	if err := os.MkdirAll(dst, srcInfo.Mode().Perm()); err != nil {
		return err
	}
	return filepath.WalkDir(srcLocal, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(srcLocal, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(target, info.Mode().Perm())
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			os.Remove(target)
			return os.Symlink(link, target)
		default:
			perm := info.Mode().Perm()
			if fileMode != 0 {
				perm = fileMode
			}
			return b.PutFile(path, target, perm)
		}
	})
}

// localCmd is the Commander half of LocalBackend.
type localCmd struct{ *LocalBackend }

func (localCmd) Run(name string, args ...string) Result {
	return runLocal(RunOptions{}, name, args...)
}

func (localCmd) RunWithStdin(stdin []byte, name string, args ...string) Result {
	return runLocal(RunOptions{Stdin: stdin}, name, args...)
}

func (localCmd) RunWithOptions(opts RunOptions, name string, args ...string) Result {
	return runLocal(opts, name, args...)
}

func runLocal(opts RunOptions, name string, args ...string) Result {
	ctx := opts.Context
	if ctx == nil {
		ctx = context.Background()
	}
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}
	cmd := osexec.CommandContext(ctx, name, args...)
	if opts.WorkDir != "" {
		cmd.Dir = opts.WorkDir
	}
	if opts.Stdin != nil {
		cmd.Stdin = bytes.NewReader(opts.Stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exit := 0
	if err != nil {
		var ee *osexec.ExitError
		if errors.As(err, &ee) {
			exit = ee.ExitCode()
			err = nil
		} else {
			exit = -1
		}
	}
	// Surface a clear "timeout" signal to callers when context kicked
	// in. We can't return ctx.Err() directly because cmd.Run() races
	// with cancellation, so check ctx.Err() last.
	if ctxErr := ctx.Err(); ctxErr == context.DeadlineExceeded {
		err = ctxErr
		if exit == 0 {
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
