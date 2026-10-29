package service

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/opsremote"
)

// Exercise the actual shell controller with real disposable child processes.
// A private proc fixture makes it runnable on macOS as well as Linux.
func TestManagedProcessStartIsIdempotentAndRestartTargetsOnlyExactIdentity(t *testing.T) {
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	proc := filepath.Join(root, "proc")
	home := filepath.Join(root, "service with quote's")
	bin := filepath.Join(root, "bin")
	for _, dir := range []string{proc, home, bin} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"stat", "readlink"} {
		binary, err := exec.LookPath("g" + name)
		if err != nil {
			binary, err = exec.LookPath(name)
		}
		if err != nil {
			t.Skip("coreutils unavailable")
		}
		if err := os.Symlink(binary, filepath.Join(bin, name)); err != nil {
			t.Fatal(err)
		}
	}
	q := opsremote.Quote
	identity := "org.example.InspectionFixture"
	start := filepath.Join(home, "start.sh")
	script := fmt.Sprintf("#!/bin/sh\nset -eu\nd=%s/$$\nmkdir -p -- \"$d\"\nprintf '%%s\\000' %s > \"$d/cmdline\"\nln -s -- %s \"$d/cwd\"\ntrap 'rm -rf -- \"$d\"; exit 0' TERM INT EXIT\nwhile :; do sleep 1; done\n", q(proc), q(identity), q(home))
	if err := os.WriteFile(start, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		entries, _ := os.ReadDir(proc)
		for _, e := range entries {
			if pid, err := strconv.Atoi(e.Name()); err == nil {
				p, _ := os.FindProcess(pid)
				if p != nil {
					_ = p.Signal(syscall.SIGTERM)
				}
			}
		}
	})
	// Similar agent/other arguments must not be selected or signalled.
	unrelated := filepath.Join(proc, "99999999")
	os.Mkdir(unrelated, 0700)
	os.WriteFile(filepath.Join(unrelated, "cmdline"), []byte("java\x00-javaagent:"+identity+"\x00"), 0600)
	os.Symlink(home, filepath.Join(unrelated, "cwd"))
	row := model.ManagedService{RunUser: current.Username, Directory: home, StartScript: start, Identity: identity}
	run := func(action string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		script := strings.ReplaceAll(serviceProcessScript(row, action), "/proc/", q(proc)+"/")
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", script)
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s failed: %v: %s", action, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if got := run("status"); got != "STOPPED" {
		t.Fatalf("unrelated process matched: %s", got)
	}
	first := run("start")
	if !strings.HasPrefix(first, "RUNNING ") {
		t.Fatal(first)
	}
	if got := run("start"); got != "ALREADY_RUNNING" {
		t.Fatalf("duplicate launch: %s", got)
	}
	second := run("restart")
	if !strings.HasPrefix(second, "RUNNING ") || first == second {
		t.Fatalf("restart did not replace exact target: %q => %q", first, second)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatal("unrelated process changed")
	}
}
