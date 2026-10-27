package nodeexporter

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/common/expfmt"
	prommodel "github.com/prometheus/common/model"
)

func fixtureSnapshot(t *testing.T, text string, at time.Time) snapshot {
	t.Helper()
	p := expfmt.NewTextParser(prommodel.UTF8Validation)
	f, e := p.TextToMetricFamilies(strings.NewReader(text))
	if e != nil {
		t.Fatal(e)
	}
	return snapshot{at, f}
}
func fixtureMetrics(n int) string {
	return fmt.Sprintf(`node_memory_MemTotal_bytes 1000
node_memory_MemAvailable_bytes 600
node_memory_SwapTotal_bytes 100
node_memory_SwapFree_bytes 80
node_cpu_seconds_total{cpu="0",mode="idle"} %d
node_cpu_seconds_total{cpu="0",mode="user"} %d
node_cpu_seconds_total{cpu="0",mode="system"} %d
node_cpu_seconds_total{cpu="0",mode="steal"} %d
node_cpu_seconds_total{cpu="0",mode="iowait"} %d
node_procs_running 4
node_procs_blocked 2
node_disk_reads_completed_total{device="dm-0"} %d
node_disk_writes_completed_total{device="dm-0"} %d
node_disk_read_time_seconds_total{device="dm-0"} %f
node_disk_write_time_seconds_total{device="dm-0"} %f
node_disk_read_bytes_total{device="dm-0"} %d
node_disk_written_bytes_total{device="dm-0"} %d
node_disk_io_time_seconds_total{device="dm-0"} %f
node_disk_io_time_weighted_seconds_total{device="dm-0"} %f
node_disk_io_now{device="dm-0"} 3
node_disk_device_mapper_info{device="dm-0",name="vg-root",uuid="LVM-x",vg_name="vg",lv_name="root"} 1
node_filesystem_size_bytes{device="/dev/mapper/vg-root",fstype="xfs",mountpoint="/"} 1000
node_filesystem_avail_bytes{device="/dev/mapper/vg-root",fstype="xfs",mountpoint="/"} 400
node_filesystem_files{device="/dev/mapper/vg-root",fstype="xfs",mountpoint="/"} 100
node_filesystem_files_free{device="/dev/mapper/vg-root",fstype="xfs",mountpoint="/"} 60
node_network_receive_bytes_total{device="eth0"} %d
node_network_transmit_bytes_total{device="eth0"} %d
node_pressure_cpu_waiting_seconds_total %f
node_exporter_build_info{version="fixture"} 1
node_uname_info{nodename="fixture",sysname="Linux",release="test"} 1
`, 800+50*n, 100+20*n, 50+10*n, 10*n, 20+20*n, 100+10*n, 50+5*n, 1+0.04*float64(n), 2+0.1*float64(n), 10000+20480*n, 10000+40960*n, 1+0.5*float64(n), 1+2*float64(n), 1024+2000*n, 2048+4000*n, 1+0.2*float64(n))
}
func TestCounterRatesLatencyAndMappings(t *testing.T) {
	now := time.Now()
	a, b := fixtureSnapshot(t, fixtureMetrics(0), now), fixtureSnapshot(t, fixtureMetrics(1), now.Add(2*time.Second))
	m, e := calculate(a, b, "http://fixture/metrics")
	if e != nil {
		t.Fatal(e)
	}
	if m.Source != "node_exporter" || math.Abs(m.CPUUsagePercent-30/110.0*100) > 1e-10 || math.Abs(m.CPUStealPercent-10/110.0*100) > 1e-10 || m.RunnableTasks == nil || *m.RunnableTasks != 4 {
		t.Fatalf("CPU %+v", m)
	}
	d := m.DiskIO[0]
	if math.Abs(*d.ReadAwaitMs-4) > 0.0001 || math.Abs(*d.WriteAwaitMs-20) > 0.0001 || d.ReadIOPS != 5 || d.AverageQueue != 1 || d.InFlight != 3 || d.Kind != "lvm" || d.MappedName != "vg-root" || len(d.Mountpoints) != 1 || d.Mountpoints[0] != "/" {
		t.Fatalf("disk %+v", d)
	}
	if m.CPUIntervalPressurePercent == nil || math.Abs(*m.CPUIntervalPressurePercent-10) > 0.001 || len(m.Networks) != 1 || m.Networks[0].ReceivePerSecond != 1000 || m.Disks[0].InodeUsedPercent != 40 {
		t.Fatalf("other metrics %+v", m)
	}
	// No completed reads gives unavailable latency, never a fabricated healthy zero.
	b = fixtureSnapshot(t, strings.Replace(fixtureMetrics(1), "node_disk_reads_completed_total{device=\"dm-0\"} 110", "node_disk_reads_completed_total{device=\"dm-0\"} 100", 1), now.Add(2*time.Second))
	m, e = calculate(a, b, "fixture")
	if e != nil || m.DiskIO[0].ReadAwaitMs != nil {
		t.Fatalf("idle read latency %v %+v", e, m)
	}
}
func TestHTTPOnlyCollectionAndFailure(t *testing.T) {
	var count atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" {
			http.Error(w, "bad", 500)
			return
		}
		n := int(count.Add(1)) - 1
		fmt.Fprint(w, fixtureMetrics(n))
	}))
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m, e := Collect(ctx, s.URL+"/metrics")
	if e != nil || m.SourceURL != s.URL+"/metrics" || count.Load() != 2 {
		t.Fatalf("HTTP %v %+v count=%d", e, m, count.Load())
	}
	if _, e := Collect(ctx, s.URL+"/failure"); e == nil {
		t.Fatal("HTTP error hidden")
	}
}
func TestOptionalActualExporter(t *testing.T) {
	host := os.Getenv("DFOPS_TEST_EXPORTER")
	if host == "" {
		t.Skip("set DFOPS_TEST_EXPORTER for read-only live HTTP verification")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	m, e := Collect(ctx, host)
	if e != nil {
		t.Fatal(e)
	}
	if m.MemTotalBytes == 0 || m.CPUCores < 1 || len(m.DiskIO) == 0 {
		t.Fatalf("incomplete result %+v", m)
	}
	t.Log("HTTP exporter", m.ExporterVersion, "cores", strconv.Itoa(m.CPUCores), "disks", len(m.DiskIO), "PSI available", m.CPUIntervalPressurePercent != nil)
}
