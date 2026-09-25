package monitor

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
	psnet "github.com/shirou/gopsutil/v4/net"

	"github.com/ycfrp/ycfrp/internal/logx"
	"github.com/ycfrp/ycfrp/internal/store"
)

// InterfaceUsage is the per-interface byte counter delta between two samples.
type InterfaceUsage struct {
	Name      string  `json:"name"`
	RxBytes   uint64  `json:"rxBytes"`
	TxBytes   uint64  `json:"txBytes"`
	RxRate    float64 `json:"rxRate"`
	TxRate    float64 `json:"txRate"`
	RxTotal   uint64  `json:"rxTotal"`
	TxTotal   uint64  `json:"txTotal"`
	IsDefault bool    `json:"isDefault"`
}

// HostStats is a point in time view of the machine running the panel.
type HostStats struct {
	Hostname    string           `json:"hostname"`
	Platform    string           `json:"platform"`
	Arch        string           `json:"arch"`
	Uptime      uint64           `json:"uptime"`
	CPUPercent  float64          `json:"cpuPercent"`
	MemUsed     uint64           `json:"memUsed"`
	MemTotal    uint64           `json:"memTotal"`
	MemPercent  float64          `json:"memPercent"`
	DiskUsed    uint64           `json:"diskUsed"`
	DiskTotal   uint64           `json:"diskTotal"`
	DiskPercent float64          `json:"diskPercent"`
	Interfaces  []InterfaceUsage `json:"interfaces"`
	TotalRx     uint64           `json:"totalRx"`
	TotalTx     uint64           `json:"totalTx"`
	RxRate      float64          `json:"rxRate"`
	TxRate      float64          `json:"txRate"`
	UpdatedAt   time.Time        `json:"updatedAt"`
}

// Monitor samples host and interface counters on a fixed interval.
type Monitor struct {
	mu      sync.RWMutex
	logger  *logx.Logger
	series  *store.SeriesStore
	stats   HostStats
	prev    map[string]psnet.IOCountersStat
	prevAt  time.Time
	stop    chan struct{}
	running bool

	interval  time.Duration
	iface     string
	hookFn    func(HostStats)
	quotaGB   float64
	quotaSent bool
}

// New builds a monitor persisting its time series under dir.
func New(logger *logx.Logger, dir string) *Monitor {
	return &Monitor{
		logger: logger,
		series: store.NewSeriesStore(filepath.Join(dir, "traffic-series.json"), 1440),
		prev:   make(map[string]psnet.IOCountersStat),
		stop:   make(chan struct{}),
	}
}

// Series exposes the retained throughput samples.
func (m *Monitor) Series() []store.Sample { return m.series.Points() }

// SetQuota configures a monthly traffic quota in gigabytes. Zero disables it.
func (m *Monitor) SetQuota(gb float64) {
	m.mu.Lock()
	m.quotaGB = gb
	m.mu.Unlock()
}

// SetHook registers a callback invoked after every successful sample.
func (m *Monitor) SetHook(fn func(HostStats)) {
	m.mu.Lock()
	m.hookFn = fn
	m.mu.Unlock()
}

// Configure updates the sampling interval and the preferred interface.
func (m *Monitor) Configure(interval time.Duration, iface string) {
	m.mu.Lock()
	if interval >= time.Second {
		m.interval = interval
	} else {
		m.interval = 5 * time.Second
	}
	m.iface = iface
	m.mu.Unlock()
}

// Start begins sampling in the background.
func (m *Monitor) Start() {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return
	}
	if m.interval <= 0 {
		m.interval = 5 * time.Second
	}
	m.running = true
	m.stop = make(chan struct{})
	interval := m.interval
	m.mu.Unlock()

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		m.sample()
		for {
			select {
			case <-ticker.C:
				m.sample()
			case <-m.stop:
				return
			}
		}
	}()
}

// Stop halts background sampling.
func (m *Monitor) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.running {
		return
	}
	m.running = false
	close(m.stop)
}

// Stats returns the most recent host snapshot.
func (m *Monitor) Stats() HostStats {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.stats
}

func (m *Monitor) sample() {
	counters, err := psnet.IOCounters(true)
	if err != nil {
		m.logf("读取网卡流量失败: %v", err)
		return
	}
	now := time.Now()
	stats := HostStats{
		Arch:      runtime.GOARCH,
		UpdatedAt: now,
	}
	if hi, err := host.Info(); err == nil {
		stats.Hostname = hi.Hostname
		stats.Platform = strings.TrimSpace(hi.Platform + " " + hi.PlatformVersion)
		stats.Uptime = hi.Uptime
	}
	if percents, err := cpu.Percent(0, false); err == nil && len(percents) > 0 {
		stats.CPUPercent = round2(percents[0])
	}
	if vm, err := mem.VirtualMemory(); err == nil {
		stats.MemUsed = vm.Used
		stats.MemTotal = vm.Total
		stats.MemPercent = round2(vm.UsedPercent)
	}
	if du, err := disk.Usage(rootPath()); err == nil {
		stats.DiskUsed = du.Used
		stats.DiskTotal = du.Total
		stats.DiskPercent = round2(du.UsedPercent)
	}

	defaultName := m.defaultInterface()
	preferred := m.preferredInterface()

	m.mu.Lock()
	prev := m.prev
	prevAt := m.prevAt
	interfaces := make([]InterfaceUsage, 0, len(counters))
	var totalRx, totalTx uint64
	var totalRxRate, totalTxRate float64
	next := make(map[string]psnet.IOCountersStat, len(counters))
	elapsed := now.Sub(prevAt).Seconds()
	if elapsed <= 0 {
		elapsed = 1
	}

	for _, c := range counters {
		if c.Name == "lo" || strings.HasPrefix(c.Name, "Loopback") {
			continue
		}
		next[c.Name] = c
		item := InterfaceUsage{
			Name:    c.Name,
			RxTotal: c.BytesRecv,
			TxTotal: c.BytesSent,
		}
		if old, ok := prev[c.Name]; ok && !prevAt.IsZero() {
			if c.BytesRecv >= old.BytesRecv {
				item.RxBytes = c.BytesRecv - old.BytesRecv
			}
			if c.BytesSent >= old.BytesSent {
				item.TxBytes = c.BytesSent - old.BytesSent
			}
			item.RxRate = round2(float64(item.RxBytes) / elapsed)
			item.TxRate = round2(float64(item.TxBytes) / elapsed)
		}
		item.IsDefault = c.Name == defaultName
		if preferred != "" && c.Name == preferred {
			item.IsDefault = true
		}
		interfaces = append(interfaces, item)
		totalRx += item.RxBytes
		totalTx += item.TxBytes
		totalRxRate += item.RxRate
		totalTxRate += item.TxRate
	}
	m.prev = next
	m.prevAt = now
	m.mu.Unlock()

	sort.SliceStable(interfaces, func(i, j int) bool {
		if interfaces[i].IsDefault != interfaces[j].IsDefault {
			return interfaces[i].IsDefault
		}
		return interfaces[i].Name < interfaces[j].Name
	})

	stats.Interfaces = interfaces
	stats.TotalRx = totalRx
	stats.TotalTx = totalTx
	stats.RxRate = round2(totalRxRate)
	stats.TxRate = round2(totalTxRate)

	m.mu.Lock()
	m.stats = stats
	hook := m.hookFn
	quota := m.quotaGB
	m.mu.Unlock()

	m.series.Append(store.Sample{
		Time:   now,
		RxRate: stats.RxRate,
		TxRate: stats.TxRate,
		Rx:     totalRx,
		Tx:     totalTx,
	})

	if hook != nil {
		hook(stats)
	}
	if quota > 0 {
		usedGB := float64(stats.TotalRx+stats.TotalTx) / (1024 * 1024 * 1024)
		if usedGB >= quota {
			m.mu.Lock()
			already := m.quotaSent
			m.quotaSent = true
			m.mu.Unlock()
			if !already {
				m.logf("本月流量已达 %.2f GB，超过设定的 %.2f GB 额度", usedGB, quota)
			}
		}
	}
}

// ResetCounters discards the delta baseline so the next sample starts fresh.
func (m *Monitor) ResetCounters() {
	m.mu.Lock()
	m.prev = make(map[string]psnet.IOCountersStat)
	m.prevAt = time.Time{}
	m.quotaSent = false
	m.mu.Unlock()
	m.series.Reset()
}

func (m *Monitor) preferredInterface() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.iface
}

func (m *Monitor) defaultInterface() string {
	conn, err := net.Dial("udp", "223.5.5.5:53")
	if err != nil {
		return ""
	}
	defer conn.Close()
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return ""
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			if ipnet.IP.Equal(addr.IP) {
				return iface.Name
			}
		}
	}
	return ""
}

func rootPath() string {
	if runtime.GOOS == "windows" {
		sysDrive := os.Getenv("SystemDrive")
		if sysDrive != "" {
			return sysDrive + `\`
		}
		return `C:\`
	}
	return "/"
}

func round2(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100
}

func (m *Monitor) logf(format string, args ...any) {
	if m.logger == nil {
		return
	}
	m.logger.Log(logx.LevelWarn, "监控", strings.TrimSpace(strings.ReplaceAll(
		strings.Trim(strings.ReplaceAll(format, "\n", " "), " "), "  ", " ")))
}
