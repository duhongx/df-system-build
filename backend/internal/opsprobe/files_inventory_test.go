package opsprobe

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestArchiveDirectoryOnlyRecognizesLocalCopyDestination(t *testing.T) {
	for _, test := range []struct{ command, want string }{
		{`test ! -f /wal/%f && cp %p /wal/%f`, "/wal"},
		{`/usr/bin/cp "%p" '/archive/%f'`, "/archive"},
		{`rsync %p user@backup:/wal/%f`, ""},
		{`/opt/archive.sh %p /wal/%f`, ""},
		{`cp %p "$ARCHIVE/%f"`, ""},
	} {
		if got := archiveDirectory(test.command); got != test.want {
			t.Fatalf("%q: %q", test.command, got)
		}
	}
}

func TestFileInventoryKeepsActualFilesAgeAndReadFailures(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "old-wal")
	if err := os.WriteFile(name, []byte("wal"), 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-8 * 24 * time.Hour)
	os.Chtimes(name, old, old)
	o := inspectFileInventory(context.Background(), dir, "cloudhis-19", "archive", 7)
	if o.Status != "WARNING" || len(o.Tables[0].Rows) != 1 {
		t.Fatalf("actual old file not preserved: %+v", o)
	}
	o = inspectFileInventory(context.Background(), filepath.Join(dir, "missing"), "cloudhis-4", "backup", 0)
	if o.Status != "UNKNOWN" || len(o.Gaps) == 0 {
		t.Fatal("missing directory became no backups")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if o := inspectFileInventory(ctx, dir, "cloudhis-19", "archive", 7); o.Status != "UNKNOWN" {
		t.Fatal("canceled inventory became complete")
	}
}

func TestJavaPathsUseActualArgumentsAndWorkingDirectory(t *testing.T) {
	dirs := javaInspectionDirectory([]string{"java", "-Dlogging.file.name=logs/app.log", "-XX:HeapDumpPath=/var/dumps/java.hprof", "--logging.file.path=/data/service-logs"}, "/srv/app", "pipe:[123]")
	want := []string{"/data/service-logs", "/srv/app/log", "/srv/app/logs"}
	if !reflect.DeepEqual(dirs, want) {
		t.Fatalf("%v", dirs)
	}
}

func TestJavaHeapDumpUsesWorkingDirectoryByDefaultWithoutTreatingItAsLogs(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"java", "-jar", "app.jar"}, "/srv/app"},
		{[]string{"java", "-XX:HeapDumpPath=dumps/java.hprof"}, "/srv/app/dumps"},
		{[]string{"java", "-XX:HeapDumpPath=/var/dumps"}, "/var/dumps"},
	} {
		got := javaHeapInspectionDirectories(test.args, "/srv/app")
		if len(got) != 1 || got[0] != test.want {
			t.Fatalf("heap location: %v", got)
		}
		for _, dir := range javaInspectionDirectory(test.args, "/srv/app", "pipe:[123]") {
			if dir == test.want {
				t.Fatalf("heap directory %s was counted as a log directory", dir)
			}
		}
	}
}

func TestJavaHeapScanDoesNotHideAnotherProcessLogDirectory(t *testing.T) {
	root := t.TempDir()
	shared, other := filepath.Join(root, "shared"), filepath.Join(root, "other")
	for _, dir := range []string{shared, other} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(shared, "java_pid100.hprof"), []byte("dump"), 0600); err != nil {
		t.Fatal(err)
	}
	procRoot := filepath.Join(root, "proc")
	for _, p := range []struct{ pid, cwd, extra string }{{"100", shared, ""}, {"200", other, "--logging.file.path=" + shared}} {
		base := filepath.Join(procRoot, p.pid)
		if err := os.MkdirAll(filepath.Join(base, "fd"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(base, "cmdline"), []byte("java\x00-jar\x00app.jar\x00"+p.extra+"\x00"), 0600); err != nil {
			t.Fatal(err)
		}
		for link, target := range map[string]string{"cwd": p.cwd, "root": "/", "fd/1": "pipe:[fixture]"} {
			if err := os.Symlink(target, filepath.Join(base, link)); err != nil {
				t.Fatal(err)
			}
		}
	}
	observations := inspectJavaLogsFrom(context.Background(), procRoot)
	logs, heap := observations[0], observations[1]
	if logs.Status != "RECORDED" || len(logs.Tables[0].Rows) != 1 || logs.Tables[0].Rows[0][1] != shared {
		t.Fatalf("heap-only scan hid actual logs: %+v", logs)
	}
	if heap.Status != "WARNING" || len(heap.Tables[0].Rows) != 1 {
		t.Fatalf("default heap dump was missing or duplicated: %+v", heap)
	}
}
