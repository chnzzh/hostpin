package collector

import (
	"context"
	"fmt"
	"math"
	"net"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/chnzzh/hostpin/internal/model"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	gnet "github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
	"github.com/shirou/gopsutil/v4/sensors"
)

type netSnapshot struct {
	rx, tx uint64
	at     time.Time
}

type diskSnapshot struct {
	readBytes, writeBytes uint64
	readCount, writeCount uint64
	readTime, writeTime   uint64
	ioTime                uint64
	at                    time.Time
}

type Collector struct {
	mu          sync.Mutex
	includeNICs map[string]struct{}
	excludeNICs map[string]struct{}
	mounts      map[string]struct{}
	enableGPU   bool
	networkPrev map[string]netSnapshot
	diskPrev    map[string]diskSnapshot
	sequence    uint64
	lastBootID  string
	lastFull    model.MetricSample
}

func New(cfg model.AgentConfig) *Collector {
	collector := &Collector{
		networkPrev: make(map[string]netSnapshot), diskPrev: make(map[string]diskSnapshot),
	}
	collector.UpdateConfig(cfg)
	return collector
}

func (c *Collector) UpdateConfig(cfg model.AgentConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.includeNICs = stringSet(cfg.IncludeNICs)
	c.excludeNICs = stringSet(cfg.ExcludeNICs)
	c.mounts = stringSet(cfg.IncludeMountpoints)
	c.enableGPU = cfg.EnableGPU
}

func stringSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result[value] = struct{}{}
		}
	}
	return result
}

func Identity(ctx context.Context, version string) model.AgentIdentity {
	identity := model.AgentIdentity{Version: version, OS: runtime.GOOS, Arch: runtime.GOARCH}
	if hostname, err := host.InfoWithContext(ctx); err == nil {
		identity.Hostname = hostname.Hostname
		identity.OS = firstNonEmpty(hostname.Platform, identity.OS)
		if hostname.PlatformVersion != "" {
			identity.OS += " " + hostname.PlatformVersion
		}
		identity.KernelVersion = hostname.KernelVersion
		identity.Virtualization = firstNonEmpty(hostname.VirtualizationSystem, hostname.VirtualizationRole)
	}
	if info, err := cpu.InfoWithContext(ctx); err == nil && len(info) > 0 {
		identity.CPUName = strings.TrimSpace(info[0].ModelName)
	}
	identity.CPUCores = runtime.NumCPU()
	identity.IPv4, identity.IPv6 = publicAddresses()
	return identity
}

func publicAddresses() (string, string) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", ""
	}
	var ipv4, ipv6 string
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, _ := iface.Addrs()
		for _, address := range addresses {
			ip, _, err := net.ParseCIDR(address.String())
			if err != nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			if ip.To4() != nil && ipv4 == "" {
				ipv4 = ip.String()
			} else if ip.To4() == nil && ipv6 == "" {
				ipv6 = ip.String()
			}
		}
	}
	return ipv4, ipv6
}

func (c *Collector) Collect(ctx context.Context, full bool) (model.MetricSample, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now().UTC()
	c.sequence++
	sample := c.lastFull
	sample.Sequence = c.sequence
	sample.CollectedAt = now
	sample.Message = ""

	if values, err := cpu.PercentWithContext(ctx, 0, false); err == nil && len(values) > 0 && !math.IsNaN(values[0]) {
		sample.CPU = clamp(values[0], 0, 100)
	}
	if memory, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		sample.MemoryTotal, sample.MemoryUsed = memory.Total, memory.Used
	}
	if swap, err := mem.SwapMemoryWithContext(ctx); err == nil {
		sample.SwapTotal, sample.SwapUsed = swap.Total, swap.Used
	}
	if averages, err := load.AvgWithContext(ctx); err == nil {
		sample.Load1, sample.Load5, sample.Load15 = averages.Load1, averages.Load5, averages.Load15
	}
	if uptime, err := host.UptimeWithContext(ctx); err == nil {
		sample.UptimeSeconds = uptime
	}
	sample.BootID = bootID(ctx)
	if c.lastBootID != "" && sample.BootID != "" && c.lastBootID != sample.BootID {
		clear(c.networkPrev)
		clear(c.diskPrev)
	}
	if sample.BootID != "" {
		c.lastBootID = sample.BootID
	}
	c.collectNetwork(ctx, now, &sample)

	if full || c.lastFull.CollectedAt.IsZero() {
		c.collectDisks(ctx, now, &sample)
		if pids, err := process.PidsWithContext(ctx); err == nil {
			sample.Processes = len(pids)
		}
		if tcp, udp, err := connectionCounts(ctx); err == nil {
			sample.TCPConnections, sample.UDPConnections = tcp, udp
		}
		sample.Temperature = 0
		if temperatures, err := sensors.SensorsTemperatures(); err == nil {
			for _, sensor := range temperatures {
				if sensor.Temperature > sample.Temperature && sensor.Temperature < 150 {
					sample.Temperature = sensor.Temperature
				}
			}
		}
		if c.enableGPU {
			sample.GPUs = collectGPUs(ctx)
		} else {
			sample.GPUs = nil
		}
		c.lastFull = sample
	}
	return sample, nil
}

func (c *Collector) collectNetwork(ctx context.Context, now time.Time, sample *model.MetricSample) {
	counters, err := gnet.IOCountersWithContext(ctx, true)
	if err != nil {
		return
	}
	metrics := make([]model.NetworkMetric, 0, len(counters))
	var totalRx, totalTx uint64
	var rxRate, txRate float64
	for _, counter := range counters {
		if !c.includeNIC(counter.Name) {
			continue
		}
		metric := model.NetworkMetric{Interface: counter.Name, RxBytes: counter.BytesRecv, TxBytes: counter.BytesSent}
		if previous, ok := c.networkPrev[counter.Name]; ok {
			metric.RxBPS = counterRate(counter.BytesRecv, previous.rx, now, previous.at)
			metric.TxBPS = counterRate(counter.BytesSent, previous.tx, now, previous.at)
		}
		c.networkPrev[counter.Name] = netSnapshot{rx: counter.BytesRecv, tx: counter.BytesSent, at: now}
		totalRx += counter.BytesRecv
		totalTx += counter.BytesSent
		rxRate += metric.RxBPS
		txRate += metric.TxBPS
		metrics = append(metrics, metric)
	}
	sample.Networks = metrics
	sample.NetRxBytes, sample.NetTxBytes = totalRx, totalTx
	sample.MonthlyRxBytes, sample.MonthlyTxBytes = totalRx, totalTx
	sample.NetRxBPS, sample.NetTxBPS = rxRate, txRate
}

func (c *Collector) includeNIC(name string) bool {
	if len(c.includeNICs) > 0 {
		_, ok := c.includeNICs[name]
		return ok
	}
	if _, excluded := c.excludeNICs[name]; excluded {
		return false
	}
	lower := strings.ToLower(name)
	for _, prefix := range []string{"lo", "docker", "veth", "br-", "virbr", "utun"} {
		if strings.HasPrefix(lower, prefix) {
			return false
		}
	}
	return true
}

func (c *Collector) collectDisks(ctx context.Context, now time.Time, sample *model.MetricSample) {
	partitions, err := disk.PartitionsWithContext(ctx, false)
	if err != nil {
		return
	}
	ioCounters, _ := disk.IOCountersWithContext(ctx)
	seen := map[string]struct{}{}
	seenCapacity := map[string]struct{}{}
	metrics := make([]model.DiskMetric, 0, len(partitions))
	var total, used uint64
	for _, partition := range partitions {
		if len(c.mounts) > 0 {
			if _, ok := c.mounts[partition.Mountpoint]; !ok {
				continue
			}
		}
		if shouldSkipFilesystem(partition.Fstype, partition.Mountpoint) {
			continue
		}
		usage, err := disk.UsageWithContext(ctx, partition.Mountpoint)
		if err != nil || usage.Total == 0 {
			continue
		}
		key := partition.Device + "\x00" + partition.Mountpoint
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		metric := model.DiskMetric{Mountpoint: partition.Mountpoint, Filesystem: partition.Fstype, Total: usage.Total, Used: usage.Used}
		device := filepath.Base(partition.Device)
		if io, ok := ioCounters[device]; ok {
			previous := c.diskPrev[device]
			if !previous.at.IsZero() && now.After(previous.at) {
				metric.ReadBPS = counterRate(io.ReadBytes, previous.readBytes, now, previous.at)
				metric.WriteBPS = counterRate(io.WriteBytes, previous.writeBytes, now, previous.at)
				metric.ReadIOPS = counterRate(io.ReadCount, previous.readCount, now, previous.at)
				metric.WriteIOPS = counterRate(io.WriteCount, previous.writeCount, now, previous.at)
				operations := counterDelta(io.ReadCount, previous.readCount) + counterDelta(io.WriteCount, previous.writeCount)
				busyTime := counterDelta(io.ReadTime, previous.readTime) + counterDelta(io.WriteTime, previous.writeTime)
				if operations > 0 {
					metric.AwaitMS = float64(busyTime) / float64(operations)
				}
				elapsedMS := float64(now.Sub(previous.at).Milliseconds())
				if elapsedMS > 0 {
					metric.Utilization = clamp(float64(counterDelta(io.IoTime, previous.ioTime))/elapsedMS*100, 0, 100)
				}
			}
			c.diskPrev[device] = diskSnapshot{readBytes: io.ReadBytes, writeBytes: io.WriteBytes, readCount: io.ReadCount, writeCount: io.WriteCount, readTime: io.ReadTime, writeTime: io.WriteTime, ioTime: io.IoTime, at: now}
		}
		capacityKey := diskCapacityKey(runtime.GOOS, partition.Device, partition.Fstype, partition.Mountpoint, usage.Total)
		if _, exists := seenCapacity[capacityKey]; !exists {
			total += usage.Total
			used += usage.Used
			seenCapacity[capacityKey] = struct{}{}
		}
		metrics = append(metrics, metric)
	}
	sample.Disks, sample.DiskTotal, sample.DiskUsed = metrics, total, used
}

func diskCapacityKey(goos, device, filesystem, mountpoint string, total uint64) string {
	// APFS exposes every volume in a shared container as a partition with the
	// container's full capacity. Group equal-size APFS views so the aggregate
	// does not multiply the physical disk by System/Data/Preboot volumes.
	if goos == "darwin" && strings.EqualFold(filesystem, "apfs") {
		return fmt.Sprintf("apfs-container:%d", total)
	}
	if device != "" {
		return device
	}
	return fmt.Sprintf("%s:%s", filesystem, mountpoint)
}

func shouldSkipFilesystem(filesystem, mountpoint string) bool {
	filesystem = strings.ToLower(filesystem)
	if slices.Contains([]string{"proc", "sysfs", "devtmpfs", "devfs", "tmpfs", "overlay", "squashfs", "nsfs", "cgroup", "cgroup2", "autofs"}, filesystem) {
		return true
	}
	for _, prefix := range []string{"/proc", "/sys", "/dev", "/run"} {
		if mountpoint == prefix || strings.HasPrefix(mountpoint, prefix+"/") {
			return true
		}
	}
	return false
}

func clamp(value, minimum, maximum float64) float64 {
	return min(max(value, minimum), maximum)
}

func counterDelta(current, previous uint64) uint64 {
	if current < previous {
		return 0
	}
	return current - previous
}

func counterRate(current, previous uint64, now, before time.Time) float64 {
	if before.IsZero() || !now.After(before) || current < previous {
		return 0
	}
	return float64(current-previous) / now.Sub(before).Seconds()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
