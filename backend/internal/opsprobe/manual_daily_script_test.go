package opsprobe

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestManualDailyScriptRecordsMidnightOnceAndRejectsOtherTimes(t *testing.T) {
	bin := t.TempDir()
	file := filepath.Join(t.TempDir(), "records", "daily.log")
	for name, body := range map[string]string{"date": "#!/bin/sh\ncase \"$1\" in +%H:%M) echo \"${SAMPLE_TIME:-00:00}\";; +%Y-%m-%d) echo 2026-10-28;; esac\n", "df": "#!/bin/sh\necho '/dev/mapper/openeuler-root 1000 250 750 25% /'\n"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	script := filepath.Join("..", "..", "..", "scripts", "inspection_minio_daily.sh")
	run := func(at string) error {
		cmd := exec.Command("sh", script, file)
		cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "SAMPLE_TIME="+at)
		return cmd.Run()
	}
	if err := run("00:00"); err != nil {
		t.Fatal(err)
	}
	if err := run("00:00"); err != nil {
		t.Fatal(err)
	}
	if err := run("12:00"); err == nil {
		t.Fatal("non-midnight sample accepted")
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "2026-10-28 250\n" || strings.Count(string(data), "2026-10-28") != 1 {
		t.Fatalf("daily data missing or overwritten %s %v", data, err)
	}
}
