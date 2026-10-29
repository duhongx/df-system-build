package opsprobe

import (
	"fmt"
	"testing"
)

func TestDiscoveryUsesActualServiceEntrypoints(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{{[]string{"/usr/bin/java", "-javaagent:/opt/skywalking/agent.jar", "business.Main"}, ""}, {[]string{"/usr/bin/java", "org.elasticsearch.bootstrap.Elasticsearch"}, "elasticsearch"}, {[]string{"/opt/PostgreSQL/14/bin/postgres", "-D", "/data"}, "postgresql"}, {[]string{"/bin/redis-server", "*:6379"}, "redis"}, {[]string{"/bin/beam.smp", "-s", "rabbit"}, "rabbitmq"}}
	for _, v := range tests {
		if got := discoveredService(v.args); got != v.want {
			t.Fatalf("%v: got %s", v.args, got)
		}
	}
}

func TestHostInspectionDoesNotTurnRuntimeMountsIntoHundredsOfChecks(t *testing.T) {
	m := &HostMetrics{Disks: []Disk{{Mountpoint: "/", UsedPercent: 40, Inodes: 100, InodeUsedPercent: 20}}}
	for i := 0; i < 150; i++ {
		m.Disks = append(m.Disks, Disk{Mountpoint: fmt.Sprintf("/var/lib/kubelet/pods/pod-%d/volumes/data", i), UsedPercent: 99, Inodes: 100, InodeUsedPercent: 99})
	}
	checks := HostChecks(m)
	if len(checks) != 5 {
		t.Fatalf("runtime mounts inflated report checks: %d", len(checks))
	}
	for _, c := range checks {
		if c.Status == "WARNING" || c.Status == "CRITICAL" {
			t.Fatalf("runtime-only capacity became a host warning: %+v", c)
		}
	}
}
