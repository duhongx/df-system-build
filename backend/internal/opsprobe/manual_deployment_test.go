package opsprobe

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"df-build-server/internal/inspection"
	"df-build-server/internal/testutil"
	"github.com/jackc/pgx/v5"
)

func TestDeploymentNacosMatchesActualServerAndManagementMode(t *testing.T) {
	server := []string{"/opt/java/bin/java", "-Dnacos.home=/opt/nacos", "-jar", "/opt/nacos/target/nacos-server.jar"}
	for group, want := range map[string]string{"1:name=systemd:/system.slice/nacos.service": "systemd / nacos.service", "0::/kubepods.slice/pod-1/container": "Kubernetes", "0::/docker/container": "Docker", "1:name=systemd:/system.slice/crond.service": "宿主机进程"} {
		p := inspectProcess("123", server, group)
		if p.Home != "/opt/nacos" || p.Management != want {
			t.Fatalf("wrong process identity %+v", p)
		}
	}
	for _, args := range [][]string{{"java", "-Dnacos.server-addr=server:8848", "-jar", "api.jar"}, {"java", "-Dnacos.home=/tmp/client", "-jar", "api.jar"}, {"java", "-cp", "nacos-client.jar", "com.example.App"}} {
		if p := inspectProcess("123", args, "/system.slice/app.service"); p.Home != "" {
			t.Fatalf("client misclassified as server %+v", p)
		}
	}
}

func TestDeploymentNativePostgreSQLDistinguishesLogicalConsumerArchiveAndMissingConfiguration(t *testing.T) {
	now := time.Now()
	f := nativePostgreSQLFacts{Endpoint: "host:5432/his", Version: "14.8", StatsVisible: true, LogicalPeers: 1, ArchiveMode: "on", ArchiveConfigured: true, Archived: 1567, LastArchived: &now}
	e := inspection.Execution{Kind: "sql", ExitCode: 0, Stdout: "actual result"}
	got := nativePostgreSQLObservations("pg", f, e, nil)
	if got["cloudhis-17"].Status != "NOT_APPLICABLE" || got["cloudhis-18"].Status != "NOT_APPLICABLE" {
		t.Fatal("logical consumer treated as physical HA")
	}
	if got["cloudhis-19"].Status != "RECORDED" || !strings.Contains(got["cloudhis-19"].Value, "1567") {
		t.Fatal("active archiving was hidden")
	}
	f.ArchiveMode = "off"
	f.ArchiveConfigured = false
	got = nativePostgreSQLObservations("pg", f, e, nil)
	if got["cloudhis-19"].Status != "WARNING" || got["cloudhis-19"].ConfigurationState != "NOT_CONFIGURED" || len(got["cloudhis-19"].Gaps) > 0 {
		t.Fatal("configuration absence became failed collection")
	}
	f.StatsVisible = false
	got = nativePostgreSQLObservations("pg", f, e, nil)
	if got["cloudhis-18"].Status != "UNKNOWN" {
		t.Fatal("insufficient stats privilege treated as no replication")
	}
	got = nativePostgreSQLObservations("pg", f, inspection.Execution{ExitCode: 1}, fmt.Errorf("connection unavailable"))
	if got["cloudhis-16"].Status != "UNKNOWN" || len(got["cloudhis-16"].Gaps) == 0 {
		t.Fatal("connection failure masked")
	}
}

func TestDeploymentNativePostgreSQLReadsRealRegisteredInstance(t *testing.T) {
	dsn, ok := testutil.PostgresDSNFromEnv()
	if !ok {
		t.Skip("requires local PostgreSQL fixture")
	}
	p, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	binding := InspectionDatabaseBinding{Name: "local fixture", Config: &DatabaseConfig{Host: p.Host, Port: int(p.Port), Database: p.Database, Username: p.User, Password: p.Password, SSLMode: "disable"}}
	f, e, err := inspectNativePostgreSQL(context.Background(), binding)
	if err != nil || e.ExitCode != 0 || !f.StatsVisible || f.Version == "" {
		t.Fatalf("actual metadata missing %+v %v", f, err)
	}
	var saved nativePostgreSQLFacts
	if json.Unmarshal([]byte(e.Stdout), &saved) != nil || saved.ArchiveMode != "off" {
		t.Fatalf("actual archive configuration not preserved %s", e.Stdout)
	}
}

func TestDeploymentMinIODefaultAliasAndNoCredentialsListing(t *testing.T) {
	for _, code := range []string{"cloudhis-3", "cloudhis-38"} {
		commands := (ManualOptions{}).Commands(code)
		if len(commands) != 1 || commands[0].Text != "mc admin info 'prod'" || commands[0].Missing != "" {
			t.Fatalf("unexpected MinIO command %+v", commands)
		}
	}
}

func TestDeploymentNativeNacosLogPathDoesNotEscapeProcessRoot(t *testing.T) {
	actual := filepath.Join("/proc", "123", "root", "/home/nacos/logs")
	if actual != "/proc/123/root/home/nacos/logs" {
		t.Fatalf("unexpected namespace path %s", actual)
	}
	if os.Getenv("DFOPS_LINUX_COMPONENT_TEST") == "" {
		return
	}
	// Optional real Linux smoke check, without creating/modifying service files.
	processes, err := localInspectionProcesses(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = inspectLocalNacos(processes, "")
}

func TestDeploymentFilesystemIncludesPersistentRemoteStorageAndOmitsContainerOverlay(t *testing.T) {
	for _, v := range [][2]string{{"/dev/mapper/root", "xfs"}, {"192.0.2.1:/data", "nfs4"}, {"//server/share", "cifs"}} {
		if !inspectionFilesystem(v[0], v[1]) {
			t.Fatalf("persistent storage omitted %v", v)
		}
	}
	for _, v := range [][2]string{{"overlay", "overlay"}, {"tmpfs", "tmpfs"}, {"/dev/loop0", "squashfs"}, {"proc", "proc"}} {
		if inspectionFilesystem(v[0], v[1]) {
			t.Fatalf("virtual runtime mount included %v", v)
		}
	}
}
