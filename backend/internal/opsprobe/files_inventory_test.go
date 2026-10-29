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
	want := []string{"/data/service-logs", "/srv/app/log", "/srv/app/logs", "/var/dumps"}
	if !reflect.DeepEqual(dirs, want) {
		t.Fatalf("%v", dirs)
	}
}
