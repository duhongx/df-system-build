// Package exec defines the execution backend abstraction used by
// dfctl's ActionExecutor. The backend is the seam between "decide what
// to do" (rendered ActionSpecs) and "actually do it on a target host"
// (filesystem ops + commands).
//
// Two implementations are provided:
//
//	LocalBackend  — wraps os.* and os/exec. Used for tests, dev, and
//	                when a host happens to be the deploy node itself.
//	RemoteBackend — wraps SFTP + ssh.Session (forthcoming).
//
// The control-side keeps reading offline resources via the standard
// library; only the target-host side of every Action goes through a
// Backend. That clean split is what lets us delete the legacy
// "rsync everything + remote dfctl" path.
package exec

import (
	"context"
	"os"
	"time"
)

// Filesystem abstracts a target host's filesystem.
//
// All paths are absolute paths on the target host. Sources that come
// from the control node's offline-resource directory are still read
// via os.* — those never appear in this interface.
type Filesystem interface {
	Stat(path string) (os.FileInfo, error)
	MkdirAll(path string, perm os.FileMode) error
	WriteFile(path string, data []byte, perm os.FileMode) error
	ReadFile(path string) ([]byte, error)
	Remove(path string) error
	RemoveAll(path string) error
	Chmod(path string, mode os.FileMode) error
	Chown(path string, uid, gid int) error
	Symlink(oldname, newname string) error

	// PutFile uploads a single regular file from the control node's
	// local filesystem (srcLocal) to the target host (dst), creating
	// parent directories as needed and applying mode after the
	// transfer completes.
	PutFile(srcLocal, dst string, mode os.FileMode) error

	// PutDir uploads a directory tree from the control node
	// (srcLocal) to the target host (dst). Behaviour matches
	// `cp -a`: parent dirs are created, existing dst entries are
	// overwritten.
	//
	// fileMode controls regular-file permissions:
	//   - fileMode == 0: preserve each source file's permission bits
	//   - fileMode != 0: override every regular file with this mode
	// Directory permissions are always taken from the source.
	PutDir(srcLocal, dst string, fileMode os.FileMode) error
}

// Result is the outcome of a single command invocation on a target
// host. Stdout/Stderr are captured so callers can include them in
// DeployError details. Err is non-nil on transport failures (SSH dial,
// closed channel) — a non-zero ExitCode alone does NOT set Err so
// callers can decide whether failure is fatal.
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	Err      error
}

// RunOptions tunes a single command invocation. Zero values fall
// back to the platform default (no timeout, process inherits cwd).
type RunOptions struct {
	// Context, if non-nil, cancels the command when the owning deployment
	// is canceled or times out.
	Context context.Context
	// Stdin, if non-nil, is fed to the process via stdin.
	Stdin []byte
	// WorkDir sets the process's working directory on the target host.
	// "" means "default cwd" (typically the SSH user's home for
	// remote backends, or the dfctl-web process cwd for local).
	WorkDir string
	// Timeout, if > 0, cancels the command and returns a non-zero
	// ExitCode + Err once the duration elapses.
	Timeout time.Duration
}

// Commander abstracts running commands on a target host.
//
// LocalCommander runs via os/exec; RemoteCommander runs an
// ssh.Session. Implementations must NOT shell-escape arguments —
// callers pass them already-split, like exec.Command. Implementations
// MAY join them with proper quoting to send over a single ssh
// channel.
type Commander interface {
	Run(name string, args ...string) Result
	RunWithStdin(stdin []byte, name string, args ...string) Result
	RunWithOptions(opts RunOptions, name string, args ...string) Result
}

// Backend bundles a Filesystem and a Commander pointing at the same
// target host. The runtime keeps one Backend per (deployment, host)
// pair and reuses connections inside RemoteBackend.
type Backend struct {
	FS  Filesystem
	Cmd Commander
}
