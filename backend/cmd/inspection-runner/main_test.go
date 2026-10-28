package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"df-build-server/internal/opsprobe"
)

func TestManualJobExecutesCommandsAndPersistsTheirActualOutput(t *testing.T) {
	dir := t.TempDir()
	bin := t.TempDir()
	// A controlled CLI fixture verifies the real subprocess/job/result path.
	// No kubectl command is sent to any local or remote cluster.
	script := "#!/bin/sh\nprintf 'local-cli-result: %s\\n' \"$*\"\n"
	if e := os.WriteFile(filepath.Join(bin, "kubectl"), []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	cfg := opsprobe.JobConfig{Type: "host", Name: "本地执行器自检", TimeoutSeconds: 30, Manual: &opsprobe.ManualOptions{Profiles: []string{"kubernetes"}, Namespace: "prod", NacosPod: "nacos-1"}}
	if e := writeJSON(filepath.Join(dir, "config.json"), cfg); e != nil {
		t.Fatal(e)
	}
	if e := run("job", dir); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(filepath.Join(dir, "config.json")); !os.IsNotExist(e) {
		t.Fatal("private job configuration was retained")
	}
	data, e := os.ReadFile(filepath.Join(dir, "result.json"))
	if e != nil {
		t.Fatal(e)
	}
	var result opsprobe.JobResult
	if e := json.Unmarshal(data, &result); e != nil {
		t.Fatal(e)
	}
	if result.Error != "" || len(result.ReportChecks) != 6 {
		t.Fatalf("job result %+v", result)
	}
	for _, check := range result.ReportChecks {
		if len(check.Executions) == 0 {
			t.Fatalf("command not recorded %+v", check)
		}
		for _, entry := range check.Executions {
			if !strings.Contains(entry.Stdout, "local-cli-result:") || entry.ExitCode != 0 || entry.Command == "" || entry.StartedAt.IsZero() || entry.FinishedAt.IsZero() {
				t.Fatalf("actual output not saved %+v", entry)
			}
		}
	}
	status, e := os.ReadFile(filepath.Join(dir, "status.json"))
	if e != nil || !strings.Contains(string(status), "COMPLETED") {
		t.Fatalf("final job status %s / %v", status, e)
	}
}
