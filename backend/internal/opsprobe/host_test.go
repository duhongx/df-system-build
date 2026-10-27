package opsprobe

import (
	"math"
	"testing"
)

func TestCPUDeltasExcludeGuestAndUseInterval(t *testing.T) {
	a := cpuCounters("cpu 10 0 10 80 0 0 0 0 9999 8888\ncpu0 10 0 10 80 0 0 0 0\n")["cpu"]
	b := cpuCounters("cpu 70 0 30 100 20 0 0 0 11111 9999\n")["cpu"]
	p, w := cpuPercent(a, b)
	if math.Abs(p-66.6666667) > 0.001 || math.Abs(w-16.6666667) > 0.001 {
		t.Fatalf("CPU %.3f wait %.3f", p, w)
	}
	if a.total != 100 || delta(100, 10) != 0 {
		t.Fatal("counter reset or guest accounting incorrect")
	}
}
func TestNetworkAndDiskUnits(t *testing.T) {
	n := networkCounters("eth0: 1024 1 2 3 0 0 0 0 2048 1 4 5 0 0 0 0\nlo: 1 1 1 1 1 1 1 1 1 1 1 1 1 1 1 1")["eth0"]
	if n.ReceiveBytes != 1024 || n.TransmitBytes != 2048 || n.Errors != 6 || n.Drops != 8 {
		t.Fatalf("network %+v", n)
	}
	d := diskCounters("8 0 sda 10 0 20 30 40 0 50 60 0 70 80\n")["sda"]
	if d.readSectors != 20 || d.writeSectors != 50 || d.busyMs != 70 {
		t.Fatalf("disk %+v", d)
	}
}
