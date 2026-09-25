package app

import (
	"context"
	"strings"
	"time"

	"github.com/ycfrp/ycfrp/internal/config"
	"github.com/ycfrp/ycfrp/internal/logx"
	"github.com/ycfrp/ycfrp/internal/qqbot"
)

// qqTokens 是 QQ 开放平台凭证的进程级缓存，供给网关客户端与通知发送共用，
// 避免同一 AppID 反复申请凭证触发平台的频率限制。
var qqTokens = qqbot.NewTokenManager()

// QQTokens 暴露凭证管理器，供通知渠道复用同一份缓存。
func QQTokens() *qqbot.TokenManager { return qqTokens }

// initQQBot 创建网关客户端并挂到 App 上。
func (a *App) initQQBot() {
	a.QQ = qqbot.NewClient(qqTokens, a.onQQEvent, func(format string, args ...any) {
		a.Logf(logx.LevelInfo, "QQ机器人", format, args...)
	})
}

// StartQQBot 按当前配置启动事件接收。未配置或未开启监听时保持停止。
func (a *App) StartQQBot(ctx context.Context) {
	if a.QQ == nil {
		a.initQQBot()
	}
	cfg := a.Cfg.Snapshot().Notify.QQOfficial
	a.QQ.Configure(cfg.AppID, cfg.AppSecret, cfg.Sandbox, cfg.Proxy)
	if !cfg.Enable || !cfg.Listen {
		a.QQ.Stop()
		return
	}
	if strings.TrimSpace(cfg.AppID) == "" || strings.TrimSpace(cfg.AppSecret) == "" {
		a.Logf(logx.LevelWarn, "QQ机器人", "已开启事件监听，但尚未填写 AppID 或 AppSecret")
		a.QQ.Stop()
		return
	}
	a.QQ.Start(ctx)
}

// RestartQQBot 在配置变化后重新应用连接参数。
func (a *App) RestartQQBot(ctx context.Context) {
	if a.QQ == nil {
		a.initQQBot()
	}
	a.StartQQBot(ctx)
}

// QQBotStatus 返回网关连接状态，供面板展示。
func (a *App) QQBotStatus() qqbot.Status {
	if a.QQ == nil {
		return qqbot.Status{}
	}
	return a.QQ.Status()
}

// onQQEvent 处理从 QQ 平台收到的事件，把发现的 openid 落进配置。
//
// 只新增、不覆盖用户手工填写的值；同一条 openid 重复出现时只刷新时间，
// 避免事件频繁推送导致配置被反复改写。
func (a *App) onQQEvent(ev qqbot.OpenID) {
	group := strings.TrimSpace(ev.GroupOpenID)
	user := strings.TrimSpace(ev.UserOpenID)
	if group == "" && user == "" {
		return
	}

	cfg := a.Cfg.Snapshot()
	existing := cfg.Notify.QQOfficial.Discovered

	// 判断是否已记录过同一条标识。
	for _, d := range existing {
		if d.GroupOpenID == group && d.UserOpenID == user {
			return
		}
	}

	entry := config.DiscoveredOpenID{
		GroupOpenID: group,
		UserOpenID:  user,
		Kind:        ev.Kind,
		MsgID:       ev.MsgID,
		At:          ev.At.Format("2006-01-02 15:04:05"),
	}

	_ = a.Cfg.Update(func(c *config.Config) {
		// 只保留最近 50 条，避免长期运行后配置无限膨胀。
		list := append(c.Notify.QQOfficial.Discovered, entry)
		if len(list) > 50 {
			list = list[len(list)-50:]
		}
		c.Notify.QQOfficial.Discovered = list
	})

	if group != "" {
		a.Logf(logx.LevelInfo, "QQ机器人", "发现群 openid：%s（%s），可在通知设置中填入", group, ev.Kind)
	} else {
		a.Logf(logx.LevelInfo, "QQ机器人", "发现用户 openid：%s（%s），可在通知设置中填入", user, ev.Kind)
	}
}

// ClearDiscoveredOpenID 清空自动发现的 openid 记录。
func (a *App) ClearDiscoveredOpenID() {
	_ = a.Cfg.Update(func(c *config.Config) {
		c.Notify.QQOfficial.Discovered = nil
	})
}

// waitQQBotReady 在面板退出前给网关一点收尾时间。
func (a *App) stopQQBot() {
	if a.QQ != nil {
		a.QQ.Stop()
	}
	_ = time.Millisecond
}
