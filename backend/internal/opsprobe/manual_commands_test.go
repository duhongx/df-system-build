package opsprobe

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"df-build-server/internal/inspection"
)

func TestManualProfilesCoverGuideAndPreserveCommands(t *testing.T) {
	all := ManualOptions{Profiles: []string{"kubernetes", "node", "deploy", "postgresql", "postgresql_archive", "minio", "postgres_backup", "minio_backup", "postgresql_sql"}, MinIOAlias: "hospital", ArchiveDirectory: "/opt/PostgreSQL/14/archive", PostgresBackupDirectory: "/backup/pg", MinIOBackupDirectory: "/backup/minio", NacosPod: "nacos-1"}
	if len(all.Codes()) != 39 {
		t.Fatalf("guide coverage %d", len(all.Codes()))
	}
	for n := 20; n <= 35; n++ {
		code := fmt.Sprintf("cloudhis-%d", n)
		sql, e := manualSQL.ReadFile("manual_sql/" + code + ".sql")
		if e != nil || !strings.HasPrefix(strings.ToUpper(string(sql)), "SELECT") || !strings.HasSuffix(strings.TrimSpace(string(sql)), ";") {
			t.Fatalf("original SQL missing %s: %v", code, e)
		}
	}
	for n, want := range map[int]string{7: "kubectl get nodes", 8: "kubectl get pods -n kube-system", 16: "patronictl list", 37: "df -i", 39: "df -i | grep openeuler-root | awk '{print $3}'"} {
		commands := all.Commands(fmt.Sprintf("cloudhis-%d", n))
		if len(commands) != 1 || commands[0].Text != want {
			t.Fatalf("guide command changed %d: %+v", n, commands)
		}
	}
}

func TestManualExecutionRecordsFailureWithoutDiscardingOtherChecks(t *testing.T) {
	count := 0
	now := time.Now()
	execute := func(ctx context.Context, cmd string) inspection.Execution {
		count++
		e := inspection.Execution{Command: cmd, ExitCode: 0, StartedAt: now, FinishedAt: now, Stdout: "actual output"}
		if cmd == "patronictl list" {
			e.ExitCode = 127
			e.Stderr = "command not found"
			e.Error = "exit status 127"
		}
		return e
	}
	results, e := CollectManual(context.Background(), ManualOptions{Profiles: []string{"postgresql", "postgresql_archive"}, ArchiveDirectory: "/archive"}, nil, execute)
	if e != nil {
		t.Fatal(e)
	}
	var failed, recorded int
	for _, r := range results {
		if r.Status == "UNKNOWN" {
			failed++
		}
		if r.Status == "RECORDED" {
			recorded++
		}
		if r.Code == "cloudhis-16" && (len(r.Executions) != 1 || r.Executions[0].ExitCode != 127 || r.Executions[0].Stderr != "command not found") {
			t.Fatal("command failure evidence lost")
		}
	}
	if failed != 5 || recorded != 1 {
		t.Fatalf("unexpected outcomes failed=%d recorded=%d", failed, recorded)
	}
	// The identical patronictl command backs items 2,16,17,18 and executes once.
	if count != 9 {
		t.Fatalf("commands not reused correctly: %d", count)
	}
}

func TestManualCommandCaptureCompleteOutputAndCancellation(t *testing.T) {
	e := ExecuteManualCommand(context.Background(), "printf 'recorded-output'; printf 'recorded-error' >&2; exit 7")
	if e.ExitCode != 7 || e.Stdout != "recorded-output" || e.Stderr != "recorded-error" || e.Error == "" || e.FinishedAt.Before(e.StartedAt) {
		t.Fatalf("command record %+v", e)
	}
	e = ExecuteManualCommand(context.Background(), "printf '%40000s' x")
	if e.Truncated || len(e.Stdout) != 40000 {
		t.Fatalf("output not preserved: %d / %v", len(e.Stdout), e.Truncated)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	e = ExecuteManualCommand(ctx, "sleep 10")
	if e.Error == "" || time.Since(start) > 2*time.Second {
		t.Fatalf("command did not cancel: %+v", e)
	}
}

func TestManualConfigurationRequiresOnlyCommandParameters(t *testing.T) {
	for _, opts := range []ManualOptions{{Profiles: []string{"kubernetes"}}, {Profiles: []string{"postgresql_sql"}}, {Profiles: []string{"minio"}, MinIOAlias: "already-configured"}} {
		kind := "host"
		if opts.Profiles[0] == "postgresql_sql" {
			kind = "database"
		}
		if e := opts.Validate(kind); e != nil {
			t.Fatal(e)
		}
	}
	if e := (ManualOptions{Profiles: []string{"minio"}, MinIOAlias: "alias;touch /tmp/unwanted"}).Validate("host"); e == nil {
		t.Fatal("shell syntax accepted in alias")
	}
	r, e := CollectManual(context.Background(), ManualOptions{Profiles: []string{"postgresql_archive"}}, nil, func(context.Context, string) inspection.Execution {
		t.Fatal("executed without directory")
		return inspection.Execution{}
	})
	if e != nil {
		t.Fatal(e)
	}
	if len(r) != 1 || r[0].Status != "UNKNOWN" || len(r[0].Gaps) == 0 {
		t.Fatal("missing directory represented as successful")
	}
}
