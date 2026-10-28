package opsprobe

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"df-build-server/internal/inspection"
)

func TestManualGuideRulesRetainEvidenceAndFlagOnlyGuideConditions(t *testing.T) {
	patroni := "| Member | Host | Role | State | TL | Lag in MB |\n| pg-1 | 192.0.2.1 | Leader | running | 7 | |\n| pg-2 | 192.0.2.2 | Replica | running | 7 | 0 |\n"
	cases := []struct{ code, text, status, value string }{
		{"7", "NAME STATUS ROLES AGE VERSION\nnode-a Ready,SchedulingDisabled master 3d v1.24\n", "NORMAL", "0 个非 Ready"},
		{"7", "NAME STATUS ROLES AGE VERSION\nnode-a NotReady node 3d v1.24\n", "WARNING", "1 个非 Ready"},
		{"8", "NAME READY STATUS RESTARTS AGE\ncalico 1/1 Running 1 5d\n", "WARNING", "1 个需关注"},
		{"9", "NAME READY STATUS RESTARTS AGE\nnacos-1 1/1 Running 0 5d\n", "NORMAL", "0 个需关注"},
		{"10", "NAME READY STATUS RESTARTS AGE\njava-a 0/1 CrashLoopBackOff 947 (2m ago) 5d\n", "WARNING", "1 个需关注"},
		{"11", "20G ./service-a\n36G ./\n", "NORMAL", "0 个超过 20G"},
		{"11", "20.1G ./service-a\n36G ./\n", "WARNING", "1 个超过 20G"},
		{"12", "./service-a/dump.hprof\n", "WARNING", "1 个 hprof"},
		{"12", "", "NORMAL", "0 个 hprof"},
		{"13", strings.Repeat("-rw-r--r-- 1 root root 20 Oct 28 access.log\n", 7), "NORMAL", "7 个"},
		{"13", strings.Repeat("-rw-r--r-- 1 root root 20 Oct 28 access.log\n", 8), "WARNING", "8 个"},
		{"14", "REPOSITORY TAG IMAGE ID CREATED SIZE\nrepo/prod/api v1 id1 yesterday 1GB\nrepo/prod/api v2 id2 yesterday 1GB\nrepo/prod/api v3 id3 yesterday 1GB\nrepo/test/api v1 id4 yesterday 1GB\n", "WARNING", "prod 镜像共 3 个"},
		{"16", patroni, "NORMAL", "0 个状态异常"},
		{"16", strings.Replace(patroni, "Replica | running", "Replica | stopped", 1), "WARNING", "1 个状态异常"},
		{"17", patroni, "RECORDED", "当前 TL"},
		{"18", strings.Replace(patroni, "| 7 | 0 |", "| 7 | 2 |", 1), "WARNING", "1 个存在复制延迟"},
		{"37", "Filesystem Inodes IUsed IFree IUse% Mounted on\n/dev/root 100 80 20 80% /\n", "NORMAL", "0 个 inode 超过"},
		{"37", "Filesystem Inodes IUsed IFree IUse% Mounted on\n/dev/root 100 81 19 81% /\n", "WARNING", "1 个 inode 超过"},
		{"38", "● 192.0.2.1:9000\nNetwork: 1/1 OK\nDrives: 1/1 OK\n1 drive online, 0 drives offline\n", "NORMAL", "离线 0"},
		{"38", "○ 192.0.2.1:9000\nUptime: offline\n0 drives online, 1 drive offline\n", "WARNING", "离线 1"},
		{"7", "unexpected output", "UNKNOWN", ""},
		{"14", "unexpected output", "UNKNOWN", ""},
		{"39", "NaN", "UNKNOWN", ""},
	}
	for i, c := range cases {
		t.Run(fmt.Sprintf("%s-%d", c.code, i), func(t *testing.T) {
			o := inspection.Observation{Code: "cloudhis-" + c.code, Status: "RECORDED", Executions: []inspection.Execution{{Command: "guide-command", Stdout: c.text}}}
			interpretManual(&o)
			if o.Status != c.status || !strings.Contains(o.Value, c.value) || o.Executions[0].Stdout != c.text {
				t.Fatalf("classification/evidence mismatch %+v", o)
			}
		})
	}
}

func TestManualArchiveChecksOnlyTheRunningLeaderAndRecordsOldFiles(t *testing.T) {
	for _, leader := range []bool{false, true} {
		t.Run(fmt.Sprint(leader), func(t *testing.T) {
			called := false
			exec := func(_ context.Context, cmd string) inspection.Execution {
				e := inspection.Execution{Command: cmd}
				switch cmd {
				case "patronictl list":
					role := "Replica"
					if leader {
						role = "Leader"
					}
					e.Stdout = "| pg-1 | 192.0.2.1 | " + role + " | running | 7 | 0 |\n"
				case "hostname && hostname -I":
					e.Stdout = "pg-1\n192.0.2.1\n"
				default:
					called = true
					e.Stdout = fmt.Sprintf("2G /archive\n%d\t1024\t/archive/old-wal\n", time.Now().Add(-8*24*time.Hour).Unix())
				}
				return e
			}
			checks, err := CollectManual(context.Background(), ManualOptions{Profiles: []string{"postgresql_archive"}, ArchiveDirectory: "/archive"}, nil, exec)
			if err != nil {
				t.Fatal(err)
			}
			if len(checks) != 1 || called != leader {
				t.Fatal("archive was read on the wrong host")
			}
			want := "NOT_APPLICABLE"
			if leader {
				want = "WARNING"
			}
			if checks[0].Status != want {
				t.Fatalf("archive status %+v", checks[0])
			}
		})
	}
}
