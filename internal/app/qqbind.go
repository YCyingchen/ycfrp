package app

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ycfrp/ycfrp/internal/config"
	"github.com/ycfrp/ycfrp/internal/logx"
	"github.com/ycfrp/ycfrp/internal/qqbot"
)

// bindSession 保存一次进行中的扫码绑定。
//
// 会话只存在内存里：二维码几分钟就过期，没必要落盘；而且 taskId 与解密 key
// 是一对一的临时凭据，任务结束即失效。
type bindSession struct {
	TaskID    string
	Key       string
	QRURL     string
	QRDataURL string
	Source    string
	CreatedAt time.Time
	// LastStatus 记录最近一次轮询到的状态，供前端展示。
	LastStatus string
	// Done 表示该会话已终结（成功或过期），可以清理。
	Done bool
}

// bindMu 保护当前会话；同一时刻只允许一个绑定流程。
var (
	bindMu      sync.Mutex
	bindCurrent *bindSession
)

// StartQQBind 创建一个扫码绑定任务，返回二维码等展示信息。
func (a *App) StartQQBind(source string) (map[string]any, error) {
	task, err := qqbot.CreateBindTask(source)
	if err != nil {
		a.Logf(logx.LevelWarn, "QQ机器人", "创建扫码绑定任务失败：%v", err)
		return nil, err
	}
	dataURL, err := qqbot.QRCodeDataURL(task.QRURL, 320)
	if err != nil {
		return nil, err
	}

	bindMu.Lock()
	bindCurrent = &bindSession{
		TaskID:     task.TaskID,
		Key:        task.Key,
		QRURL:      task.QRURL,
		QRDataURL:  dataURL,
		Source:     source,
		CreatedAt:  task.CreatedAt,
		LastStatus: "等待扫码",
	}
	bindMu.Unlock()

	a.Logf(logx.LevelInfo, "QQ机器人", "已生成绑定二维码，请用手机 QQ 扫码完成绑定")
	return map[string]any{
		"taskId":    task.TaskID,
		"qrUrl":     task.QRURL,
		"qrDataUrl": dataURL,
		"status":    "等待扫码",
		"expiresIn": 150,
	}, nil
}

// PollQQBind 查询当前绑定进度。绑定成功后会自动把凭据写入配置并开启事件监听。
func (a *App) PollQQBind() (map[string]any, error) {
	bindMu.Lock()
	sess := bindCurrent
	bindMu.Unlock()
	if sess == nil {
		return nil, fmt.Errorf("没有进行中的绑定任务，请先点击生成二维码")
	}

	res, err := qqbot.PollBindResult(sess.TaskID, sess.Key)
	if err != nil {
		return nil, err
	}

	out := map[string]any{
		"status":     res.StatusTxt,
		"statusCode": int(res.Status),
		"appId":      res.AppID,
		"userOpenId": res.UserOpenID,
		"success":    false,
	}

	bindMu.Lock()
	sess.LastStatus = res.StatusTxt
	if res.IsTerminal() {
		sess.Done = true
	}
	bindMu.Unlock()

	if res.Status != qqbot.BindCompleted {
		return out, nil
	}

	// 绑定成功：写入配置，并顺带开启事件监听（用户接下来就要拿 openid）。
	if err := a.Cfg.Update(func(c *config.Config) {
		c.Notify.QQOfficial.AppID = res.AppID
		c.Notify.QQOfficial.AppSecret = res.AppSecret
		c.Notify.QQOfficial.Enable = true
		c.Notify.QQOfficial.Listen = true
	}); err != nil {
		return nil, fmt.Errorf("保存绑定凭据失败：%w", err)
	}
	a.applySettings()
	if a.rootCtx != nil {
		a.StartQQBot(a.rootCtx)
	}

	out["success"] = true
	out["appId"] = res.AppID
	out["appSecretMasked"] = maskSecret(res.AppSecret)
	a.Logf(logx.LevelInfo, "QQ机器人", "扫码绑定成功，AppID=%s，已自动开启事件监听", res.AppID)

	// 会话已完成，清空以免被重复轮询。
	bindMu.Lock()
	bindCurrent = nil
	bindMu.Unlock()
	return out, nil
}

// CancelQQBind 放弃当前绑定会话。
func (a *App) CancelQQBind() {
	bindMu.Lock()
	bindCurrent = nil
	bindMu.Unlock()
}

// BindSessionInfo 返回当前会话的展示信息，供前端刷新页面后恢复界面。
func (a *App) BindSessionInfo() map[string]any {
	bindMu.Lock()
	defer bindMu.Unlock()
	if bindCurrent == nil || bindCurrent.Done {
		return nil
	}
	return map[string]any{
		"taskId":    bindCurrent.TaskID,
		"qrUrl":     bindCurrent.QRURL,
		"qrDataUrl": bindCurrent.QRDataURL,
		"status":    bindCurrent.LastStatus,
		"expiresIn": int(time.Until(bindCurrent.CreatedAt.Add(150 * time.Second)).Seconds()),
	}
}

// bindSessionTTL 返回会话已存在的秒数，供调用方判断是否过期。
func bindSessionAge() int {
	bindMu.Lock()
	defer bindMu.Unlock()
	if bindCurrent == nil {
		return -1
	}
	return int(time.Since(bindCurrent.CreatedAt).Seconds())
}

// maskSecret 只露首尾，避免密钥在界面上完整出现。
func maskSecret(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= 8 {
		return strings.Repeat("*", len(s))
	}
	return s[:4] + strings.Repeat("*", 8) + s[len(s)-4:]
}
