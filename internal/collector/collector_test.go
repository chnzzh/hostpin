package collector

import (
	"testing"
	"time"

	"github.com/chnzzh/hostpin/internal/model"
)

func TestCounterRateHandlesResetAndClockAnomalies(t *testing.T) {
	before := time.Unix(100, 0)
	now := before.Add(2 * time.Second)
	if got := counterRate(300, 100, now, before); got != 100 {
		t.Fatalf("counter rate=%v, want 100", got)
	}
	if got := counterRate(10, 100, now, before); got != 0 {
		t.Fatalf("reset counter produced negative rate %v", got)
	}
	if got := counterRate(300, 100, before, now); got != 0 {
		t.Fatalf("backward clock produced rate %v", got)
	}
	if got := counterDelta(10, 100); got != 0 {
		t.Fatalf("reset counter delta=%d, want 0", got)
	}
}

func TestCollectorFiltersInterfacesAndFilesystems(t *testing.T) {
	collector := New(model.AgentConfig{ExcludeNICs: []string{"eth9"}})
	for _, name := range []string{"lo", "docker0", "veth123", "br-test", "virbr0", "utun2", "eth9"} {
		if collector.includeNIC(name) {
			t.Errorf("default or explicit exclusion accepted %q", name)
		}
	}
	if !collector.includeNIC("eth0") {
		t.Fatal("physical interface was excluded")
	}
	collector.UpdateConfig(model.AgentConfig{IncludeNICs: []string{"lo"}, ExcludeNICs: []string{"lo"}})
	if !collector.includeNIC("lo") || collector.includeNIC("eth0") {
		t.Fatal("explicit include list did not take precedence")
	}

	for _, item := range []struct{ filesystem, mountpoint string }{
		{"proc", "/proc"}, {"tmpfs", "/tmp"}, {"ext4", "/run/hostpin"}, {"overlay", "/var/lib/docker"},
	} {
		if !shouldSkipFilesystem(item.filesystem, item.mountpoint) {
			t.Errorf("pseudo-filesystem %s at %s was included", item.filesystem, item.mountpoint)
		}
	}
	if shouldSkipFilesystem("ext4", "/srv/data") {
		t.Fatal("regular data filesystem was excluded")
	}
}

func TestAPFSCapacityGrouping(t *testing.T) {
	root := diskCapacityKey("darwin", "/dev/disk3s1s1", "apfs", "/", 500_000)
	data := diskCapacityKey("darwin", "/dev/disk3s5", "apfs", "/System/Volumes/Data", 500_000)
	if root != data {
		t.Fatalf("shared APFS views were not grouped: %q != %q", root, data)
	}
	linuxA := diskCapacityKey("linux", "/dev/vda1", "ext4", "/", 500_000)
	linuxB := diskCapacityKey("linux", "/dev/vdb1", "ext4", "/data", 500_000)
	if linuxA == linuxB {
		t.Fatal("distinct Linux block devices were incorrectly grouped")
	}
}
