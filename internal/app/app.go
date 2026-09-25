package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/ycfrp/ycfrp/internal/config"
	"github.com/ycfrp/ycfrp/internal/frp"
	"github.com/ycfrp/ycfrp/internal/logx"
	"github.com/ycfrp/ycfrp/internal/monitor"
	"github.com/ycfrp/ycfrp/internal/notify"
	"github.com/ycfrp/ycfrp/internal/qqbot"
	"github.com/ycfrp/ycfrp/internal/store"
	"github.com/ycfrp/ycfrp/internal/version"
)

// App wires the configuration, the embedded kernels, the traffic monitor and
// the notification dispatcher into one runnable unit. Both the CLI and the
// Windows GUI entry points build on top of it.
type App struct {
	Version string
	Mode    string

	DataDir string

	mu    sync.RWMutex
	Cfg   *config.Config
	Log   *logx.Logger
	Tunnels *store.Collection[frp.Tunnel]
	// Instances 保存面板托管的具名 frps / frpc 实例。
	Instances *store.Collection[frp.Instance]

	Engine   *frp.Engine
	Monitor  *monitor.Monitor
	Notifier *notify.Notifier
	// QQ 是 QQ 官方机器人网关客户端，用于接收事件并自动发现 openid。
	QQ *qqbot.Client

	startedAt time.Time

	rootCtx    context.Context
	rootCancel context.CancelFunc

	subID int
	logCh <-chan logx.Entry

	updateMu   sync.RWMutex
	lastUpdate *UpdateCheckResult
}

// DefaultDataDir resolves the persistent data directory. It prefers a system
// wide location and falls back to a portable directory next to the binary.
func DefaultDataDir() string {
	if runtime.GOOS == "windows" {
		if dir, err := os.UserConfigDir(); err == nil && dir != "" {
			return filepath.Join(dir, "YCFRP")
		}
		if exe, err := os.Executable(); err == nil {
			return filepath.Join(filepath.Dir(exe), "data")
		}
		return filepath.Join(".", "data")
	}
	if canWrite("/var/lib/ycfrp") {
		return "/var/lib/ycfrp"
	}
	if canWrite("/etc/ycfrp") {
		return "/etc/ycfrp"
	}
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "data")
	}
	return filepath.Join(".", "data")
}

func canWrite(dir string) bool {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false
	}
	probe := filepath.Join(dir, ".ycfrp-write-test")
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return false
	}
	_ = f.Close()
	_ = os.Remove(probe)
	return true
}

// New prepares an application rooted at dataDir.
func New(dataDir, mode string) (*App, error) {
	if strings.TrimSpace(dataDir) == "" {
		dataDir = DefaultDataDir()
	}
	abs, err := filepath.Abs(dataDir)
	if err == nil {
		dataDir = abs
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建数据目录失败: %w", err)
	}

	cfg, err := config.Load(filepath.Join(dataDir, "config.json"))
	if err != nil {
		return nil, fmt.Errorf("读取配置失败: %w", err)
	}

	logger := logx.New(4000)
	logger.SetMinLevel(logx.LevelInfo)
	logger.SetConsoleLevel(logx.LevelInfo)
	if err := logger.SetDir(filepath.Join(dataDir, "logs")); err != nil {
		return nil, fmt.Errorf("创建日志目录失败: %w", err)
	}

	tunnels, err := store.NewCollection[frp.Tunnel](filepath.Join(dataDir, "tunnels.json"))
	if err != nil {
		return nil, fmt.Errorf("读取隧道列表失败: %w", err)
	}

	instances, err := loadInstances(dataDir, cfg, logger)
	if err != nil {
		return nil, err
	}

	a := &App{
		Version:   version.Version,
		Mode:      mode,
		DataDir:   dataDir,
		Cfg:       cfg,
		Log:       logger,
		Tunnels:   tunnels,
		Instances: instances,
		Engine:    frp.New(logger),
		Monitor:   monitor.New(logger, dataDir),
		Notifier:  notify.New(logger),
		startedAt: time.Now(),
	}
	a.rootCtx, a.rootCancel = context.WithCancel(context.Background())
	// 旧版隧道没有实例归属，升级后统一挂到默认客户端实例上。
	if err := a.adoptOrphanTunnels(); err != nil {
		return nil, err
	}
	a.applySettings()
	return a, nil
}

// StartedAt reports the process start time.
func (a *App) StartedAt() time.Time { return a.startedAt }

// Logf writes a formatted entry into the panel log.
func (a *App) Logf(level logx.Level, source, format string, args ...any) {
	a.Log.Log(level, source, fmt.Sprintf(format, args...))
}

// NewID allocates a short identifier for a new tunnel entry.
func NewID(prefix string) string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%s%d", prefix, time.Now().UnixNano())
	}
	return prefix + hex.EncodeToString(b)
}

// Uptime returns how long the panel has been running.
func (a *App) Uptime() time.Duration { return time.Since(a.startedAt) }

// applySettings pushes the current configuration into the subsystems.
func (a *App) applySettings() {
	cfg := a.Cfg.Snapshot()
	a.Notifier.Configure(cfg.Notify)
	a.Monitor.SetQuota(cfg.Monitor.MonthlyQuotaGB)
	a.Monitor.Configure(time.Duration(cfg.Monitor.IntervalSeconds)*time.Second, cfg.Monitor.Interface)
	a.Engine.Configure(a.Instances.List(), a.Tunnels.List())
	// 日志级别取所有服务端实例里最「啰嗦」的那个，保证任何一个实例调低
	// 级别后其日志都不会被面板的过滤掉。
	logLevel := parseLevel(a.verboseLogLevel(cfg))
	a.Log.SetMinLevel(logLevel)
	a.Log.SetConsoleLevel(logLevel)
	// QQ 机器人连接参数可能刚被修改，同步一次（未填 AppID 时不会发起连接）。
	if a.QQ != nil && a.rootCtx != nil {
		a.StartQQBot(a.rootCtx)
	}
}

// verboseLogLevel 在多个实例之间取最低（最详细）的日志级别。
func (a *App) verboseLogLevel(cfg *config.Config) string {
	level := cfg.FRPS.LogLevel
	for _, inst := range a.Instances.List() {
		if !inst.Enabled() {
			continue
		}
		lv := inst.Server.LogLevel
		if !inst.IsServer() {
			lv = inst.Client.LogLevel
		}
		if logRank(lv) < logRank(level) {
			level = lv
		}
	}
	return level
}

// logRank 把日志级别映射成可比较的序号，越小越详细。
func logRank(s string) int {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "trace":
		return 0
	case "debug":
		return 1
	case "info", "":
		return 2
	case "warn", "warning":
		return 3
	case "error":
		return 4
	default:
		return 2
	}
}

func parseLevel(s string) logx.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "trace":
		return logx.LevelTrace
	case "debug":
		return logx.LevelDebug
	case "warn", "warning":
		return logx.LevelWarn
	case "error":
		return logx.LevelError
	default:
		return logx.LevelInfo
	}
}

// UpdateConfig mutates the configuration under lock, persists it and reapplies
// the affected subsystem settings.
func (a *App) UpdateConfig(fn func(*config.Config)) error {
	if err := a.Cfg.Update(fn); err != nil {
		return err
	}
	a.applySettings()
	return nil
}

// BackendConfig describes one backend in Chinese for the log viewer.
func (a *App) BackendConfig() string {
	return "0.0.0.0"
}

// Start launches the embedded kernels and the traffic monitor.
func (a *App) Start() error {
	a.applySettings()
	cfg := a.Cfg.Snapshot()

	a.Logf(logx.LevelInfo, "面板", "YCFRP %s 启动中（运行模式：%s）", a.Version, a.Mode)
	a.Logf(logx.LevelInfo, "面板", "数据目录：%s", a.DataDir)

	a.Logf(logx.LevelInfo, "面板", "实例概况：服务端 %d 个，客户端 %d 个",
		len(a.serverInstances()), len(a.clientInstances()))
	if err := a.Engine.Start(a.rootCtx); err != nil {
		a.Logf(logx.LevelError, "面板", "内核启动异常：%v", err)
	}

	if cfg.Monitor.Enable {
		a.Monitor.Start()
		a.Logf(logx.LevelInfo, "监控", "主机流量监控已启动，采样间隔 %d 秒", cfg.Monitor.IntervalSeconds)
	}

	a.subID, a.logCh = a.Log.Subscribe()
	go a.consumeLogs()
	go a.watchLoop(a.rootCtx)
	a.startUpdateChecker(a.rootCtx)
	a.initQQBot()
	a.StartQQBot(a.rootCtx)
	return nil
}

// RestartKernels applies configuration and tunnel changes by restarting every
// instance. Used when connection level settings change.
func (a *App) RestartKernels() error {
	a.applySettings()
	err := a.Engine.Start(a.rootCtx)
	a.Log.Log(logx.LevelInfo, "面板", "已按最新配置重启全部实例")
	return err
}

// ApplyInstances 收敛实例状态，只重启真正发生变化的实例。
//
// 增删改单个实例走这里：把全部实例推倒重来会无谓地抖断其它实例上
// 正在运行的隧道。
func (a *App) ApplyInstances() error {
	a.applySettings()
	return a.Engine.Apply(a.rootCtx)
}

// ReloadTunnels hot applies the tunnel list when only tunnels changed.
func (a *App) ReloadTunnels() error {
	return a.Engine.ApplyTunnels(a.Tunnels.List())
}

// Snapshot returns the merged runtime status.
func (a *App) Snapshot() frp.StatusResponse {
	snap := a.Engine.Snapshot()
	byID := make(map[string]frp.Tunnel, len(snap.Tunnels))
	for _, t := range snap.Tunnels {
		byID[t.ID] = t
	}
	merged := make([]frp.Tunnel, 0, len(snap.Tunnels))
	for _, stored := range a.Tunnels.List() {
		if live, ok := byID[stored.ID]; ok {
			merged = append(merged, live)
			delete(byID, stored.ID)
		} else {
			merged = append(merged, stored)
		}
	}
	for _, leftover := range byID {
		merged = append(merged, leftover)
	}
	snap.Tunnels = merged
	return snap
}

// HostStats returns the latest host sampling result.
func (a *App) HostStats() monitor.HostStats { return a.Monitor.Stats() }

// Shutdown stops everything in a bounded amount of time.
func (a *App) Shutdown() {
	a.Log.Log(logx.LevelInfo, "面板", "正在停止 YCFRP ...")
	if a.logCh != nil {
		a.Log.Unsubscribe(a.subID)
	}
	a.stopQQBot()
	a.Monitor.Stop()
	a.Engine.Stop()
	if a.rootCancel != nil {
		a.rootCancel()
	}
	a.Log.Log(logx.LevelInfo, "面板", "YCFRP 已停止")
}

// consumeLogs forwards kernel log lines to the notification dispatcher so
// keyword based alerts work without extra polling.
func (a *App) consumeLogs() {
	for {
		select {
		case <-a.rootCtx.Done():
			return
		case entry, ok := <-a.logCh:
			if !ok {
				return
			}
			line := entry.Message + " " + entry.Raw
			kw, hit := a.Notifier.ShouldNotifyKeyword(line)
			if !hit {
				continue
			}
			if !a.Notifier.AllowRate("keyword:" + kw) {
				continue
			}
			go func(line string) {
				a.Notifier.Dispatch(notify.Message{
					Kind:   notify.EventKeyword,
					Title:  "关键词告警",
					Body:   line,
					Target: "命中的关键词：" + kw,
					Time:   time.Now(),
				})
			}(line)
		}
	}
}

// watchLoop tracks tunnel status transitions and raises notifications.
func (a *App) watchLoop(ctx context.Context) {
	ticker := time.NewTicker(4 * time.Second)
	defer ticker.Stop()
	prev := make(map[string]string)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			snap := a.Snapshot()
			cfg := a.Cfg.Snapshot()
			for _, t := range snap.Tunnels {
				old, seen := prev[t.ID]
				prev[t.ID] = t.Status
				if !seen || old == t.Status || t.Status == "" {
					continue
				}
				key := "tunnel:" + t.ID + ":" + t.Status
				switch {
				case t.Status == "online" && cfg.Notify.MonitorOffline:
					if a.Notifier.AllowRate(key) {
						a.Notifier.Dispatch(notify.Message{
							Kind:   notify.EventTunnelOnline,
							Title:  "隧道已恢复",
							Body:   fmt.Sprintf("隧道 %s 已重新上线，远程地址 %s", t.Name, t.RemoteAddr),
							Target: t.Name,
							Time:   time.Now(),
						})
					}
				case old == "online" && cfg.Notify.MonitorOffline:
					if a.Notifier.AllowRate(key) {
						a.Notifier.Dispatch(notify.Message{
							Kind:   notify.EventTunnelOffline,
							Title:  "隧道已离线",
							Body:   fmt.Sprintf("隧道 %s 当前状态为 %s", t.Name, frp.ProxyPhaseLabel(t.Status)),
							Target: t.Name,
							Time:   time.Now(),
						})
					}
				case t.Status == "start error" && cfg.Notify.MonitorErrors:
					if a.Notifier.AllowRate(key) {
						a.Notifier.Dispatch(notify.Message{
							Kind:   notify.EventTunnelError,
							Title:  "隧道启动失败",
							Body:   fmt.Sprintf("隧道 %s 启动失败：%s", t.Name, t.Err),
							Target: t.Name,
							Time:   time.Now(),
						})
					}
				}
			}
		}
	}
}
