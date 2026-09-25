package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ycfrp/ycfrp/internal/config"
	"github.com/ycfrp/ycfrp/internal/logx"
	"github.com/ycfrp/ycfrp/internal/notify"
	"github.com/ycfrp/ycfrp/internal/updater"
	"github.com/ycfrp/ycfrp/internal/version"
)

// UpdateCheckResult 是一次自动检查的结论，供面板读取最近一次状态。
type UpdateCheckResult struct {
	CheckedAt   time.Time `json:"checkedAt"`
	Latest      string    `json:"latest"`
	HasUpdate   bool      `json:"hasUpdate"`
	Err         string    `json:"err,omitempty"`
	PackageURL  string    `json:"packageUrl"`
	Platform    string    `json:"platform"`
	ReleasedAt  string    `json:"releasedAt"`
	Notes       string    `json:"notes"`
	NotifiedFor string    `json:"notifiedFor"`
}

// startUpdateChecker 按配置的间隔在后台检查新版本，并在发现更新时推送通知。
//
// 检查频率刻意放慢（默认 6 小时）：面板是常驻服务，版本源由自己控制节奏，
// 高频轮询既无必要也会给更新源带来无谓流量。
func (a *App) startUpdateChecker(ctx context.Context) {
	go func() {
		// 启动后稍作延迟再首次检查，避免和面板启动阶段争抢资源。
		select {
		case <-ctx.Done():
			return
		case <-time.After(45 * time.Second):
		}
		a.checkUpdateOnce()

		for {
			interval := a.updateCheckInterval()
			select {
			case <-ctx.Done():
				return
			case <-time.After(interval):
				a.checkUpdateOnce()
			}
		}
	}()
}

// updateCheckInterval 返回下一次检查的等待时间，最小 30 分钟。
func (a *App) updateCheckInterval() time.Duration {
	cfg := a.Cfg.Snapshot()
	if !cfg.Updates.CheckEnabled {
		// 未开启时也用较长间隔轮询，这样用户中途打开开关无需重启面板。
		return 30 * time.Minute
	}
	hours := cfg.Updates.CheckIntervalHours
	if hours <= 0 {
		hours = 6
	}
	d := time.Duration(hours) * time.Hour
	if d < 30*time.Minute {
		d = 30 * time.Minute
	}
	return d
}

// checkUpdateOnce 执行一次版本检查；发现新版本且尚未提醒过时推送通知。
func (a *App) checkUpdateOnce() {
	cfg := a.Cfg.Snapshot()
	base := strings.TrimSpace(cfg.Updates.SourceURL)
	if base == "" {
		return
	}
	// 检查功能未开启时只更新「最近检查时间」之外什么都不做。
	m, err := updater.FetchManifest(base, cfg.Updates.Proxy, 20*time.Second)

	res := UpdateCheckResult{
		CheckedAt: time.Now(),
		Platform:  updater.CurrentPlatform(),
	}
	if err != nil {
		res.Err = err.Error()
		a.setUpdateResult(res)
		a.Logf(logx.LevelWarn, "更新", "自动检查更新失败：%v", err)
		return
	}
	res.Latest = m.Version
	res.ReleasedAt = m.ReleasedAt
	res.Notes = m.Notes
	res.PackageURL = m.Platforms[res.Platform]
	res.HasUpdate = updater.CompareVersions(m.Version, version.Version) > 0

	if !cfg.Updates.CheckEnabled {
		a.setUpdateResult(res)
		return
	}

	// 记录检查时间，供面板展示。
	_ = a.Cfg.Update(func(c *config.Config) {
		c.Updates.LastCheckAt = res.CheckedAt.Format("2006-01-02 15:04:05")
	})

	if !res.HasUpdate {
		a.setUpdateResult(res)
		a.Logf(logx.LevelInfo, "更新", "自动检查：当前 %s 已是最新版本", version.Version)
		return
	}

	// 同一个版本只提醒一次，避免每隔几小时就重复推送。
	alreadyNotified := strings.TrimSpace(cfg.Updates.LastSeenVersion) == m.Version
	res.NotifiedFor = cfg.Updates.LastSeenVersion
	a.setUpdateResult(res)

	a.Logf(logx.LevelInfo, "更新", "自动检查：发现新版本 %s（当前 %s）", m.Version, version.Version)
	if alreadyNotified || !cfg.Updates.NotifyOnUpdate {
		return
	}
	if !cfg.Notify.Enable {
		a.Logf(logx.LevelWarn, "更新", "发现新版本 %s，但通知功能未启用", m.Version)
		return
	}

	body := fmt.Sprintf("当前版本 %s，最新版本 %s。请到面板「更新」页查看，Docker 部署在宿主机执行升级命令。",
		version.Version, m.Version)
	if res.Notes != "" {
		body += "\n更新说明：" + res.Notes
	}
	a.Notifier.Dispatch(notify.Message{
		Kind:   notify.EventUpdateAvailable,
		Title:  "发现新版本 " + m.Version,
		Body:   body,
		Target: version.ProductName,
		Time:   time.Now(),
	})
	_ = a.Cfg.Update(func(c *config.Config) {
		c.Updates.LastSeenVersion = m.Version
		c.Updates.LatestVersion = m.Version
	})
}

func (a *App) setUpdateResult(res UpdateCheckResult) {
	a.updateMu.Lock()
	a.lastUpdate = &res
	a.updateMu.Unlock()
}

// LastUpdateCheck 返回最近一次自动检查的结论，未检查过时为 nil。
func (a *App) LastUpdateCheck() *UpdateCheckResult {
	a.updateMu.RLock()
	defer a.updateMu.RUnlock()
	return a.lastUpdate
}

// CheckUpdateNow 供面板按钮与自动检查共用同一条逻辑。
func (a *App) CheckUpdateNow() {
	a.checkUpdateOnce()
}
