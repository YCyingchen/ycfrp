package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ycfrp/ycfrp/internal/config"
	"github.com/ycfrp/ycfrp/internal/logx"
)

// EventKind classifies why a notification is being dispatched.
type EventKind string

const (
	EventTunnelOnline  EventKind = "tunnel_online"
	EventTunnelOffline EventKind = "tunnel_offline"
	EventTunnelError   EventKind = "tunnel_error"
	EventKeyword       EventKind = "keyword"
	EventTrafficQuota  EventKind = "traffic_quota"
	EventKernel        EventKind = "kernel"
	EventTest          EventKind = "test"
	// EventUpdateAvailable 在检测到有新版本可用时发出。
	EventUpdateAvailable EventKind = "update_available"
)

// KindLabel renders the Chinese label shown in the panel and in messages.
func KindLabel(k EventKind) string {
	switch k {
	case EventTunnelOnline:
		return "隧道恢复"
	case EventTunnelOffline:
		return "隧道离线"
	case EventTunnelError:
		return "隧道异常"
	case EventKeyword:
		return "关键词告警"
	case EventTrafficQuota:
		return "流量超限"
	case EventKernel:
		return "内核事件"
	case EventUpdateAvailable:
		return "发现新版本"
	default:
		return "系统通知"
	}
}

// Message is one notification payload.
type Message struct {
	Kind     EventKind `json:"kind"`
	Title    string    `json:"title"`
	Body     string    `json:"body"`
	Target   string    `json:"target"`
	Time     time.Time `json:"time"`
}

// Text renders the message as a human readable block.
func (m Message) Text() string {
	var sb strings.Builder
	sb.WriteString("【" + m.Title + "】\n")
	if m.Target != "" {
		sb.WriteString("对象：" + m.Target + "\n")
	}
	sb.WriteString("时间：" + m.Time.Format("2006-01-02 15:04:05") + "\n")
	sb.WriteString("详情：" + m.Body)
	return sb.String()
}

// Notifier dispatches messages to every configured channel.
type Notifier struct {
	mu       sync.RWMutex
	logger   *logx.Logger
	cfg      config.NotifyConfig
	client   *http.Client
	cooldown map[string]time.Time
}

// New builds a notifier bound to the supplied logger.
func New(logger *logx.Logger) *Notifier {
	return &Notifier{
		logger:   logger,
		client:   &http.Client{Timeout: 8 * time.Second},
		cooldown: make(map[string]time.Time),
	}
}

// Configure replaces the notification settings.
func (n *Notifier) Configure(cfg config.NotifyConfig) {
	n.mu.Lock()
	n.cfg = cfg
	n.mu.Unlock()
}

// Config returns the active notification settings.
func (n *Notifier) Config() config.NotifyConfig {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.cfg
}

// Result describes the outcome of a single channel delivery attempt.
type Result struct {
	Channel string `json:"channel"`
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
}

// Test sends a test message through every enabled channel and reports the
// per-channel outcome so the panel can show a precise diagnostic.
func (n *Notifier) Test() []Result {
	msg := Message{
		Kind:   EventTest,
		Title:  "YCFRP 测试通知",
		Body:   "这是一条来自 YCFRP 面板的测试消息，收到即表示通知通道可用。",
		Target: "通知通道",
		Time:   time.Now(),
	}
	return n.Dispatch(msg)
}

// ShouldNotifyKeyword reports whether a kernel log line matches the configured
// keyword list. Matching is case insensitive and works for Chinese text too.
func (n *Notifier) ShouldNotifyKeyword(line string) (string, bool) {
	n.mu.RLock()
	cfg := n.cfg
	n.mu.RUnlock()
	if !cfg.Enable || !cfg.MonitorErrors {
		return "", false
	}
	lower := strings.ToLower(line)
	for _, kw := range cfg.Keywords {
		kw = strings.TrimSpace(kw)
		if kw == "" {
			continue
		}
		if strings.Contains(lower, strings.ToLower(kw)) {
			return kw, true
		}
	}
	return "", false
}

// AllowRate limits how often the same topic can raise a notification.
func (n *Notifier) AllowRate(key string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	quiet := time.Duration(n.cfg.QuietMinutes) * time.Minute
	if quiet <= 0 {
		quiet = 5 * time.Minute
	}
	now := time.Now()
	if last, ok := n.cooldown[key]; ok && now.Sub(last) < quiet {
		return false
	}
	n.cooldown[key] = now
	return true
}

// ResetRate clears the cooldown table, used when settings change.
func (n *Notifier) ResetRate() {
	n.mu.Lock()
	n.cooldown = make(map[string]time.Time)
	n.mu.Unlock()
}

// Dispatch delivers a message to every enabled channel in parallel.
func (n *Notifier) Dispatch(msg Message) []Result {
	n.mu.RLock()
	cfg := n.cfg
	n.mu.RUnlock()

	if !cfg.Enable {
		return []Result{{Channel: "全局开关", OK: false, Error: "通知功能未启用"}}
	}

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		results []Result
	)
	record := func(r Result) {
		mu.Lock()
		results = append(results, r)
		mu.Unlock()
	}

	if cfg.QQ.Enable {
		wg.Add(1)
		go func() {
			defer wg.Done()
			record(n.sendQQ(cfg.QQ, msg))
		}()
	}
	// 其余渠道各自独立启用，任一失败不影响其他渠道。
	channels := []struct {
		enabled bool
		send    func() Result
	}{
		{cfg.QQOfficial.Enable, func() Result { return n.sendQQOfficial(cfg.QQOfficial, msg) }},
		{cfg.WeCom.Enable, func() Result { return n.sendWeCom(cfg.WeCom, msg) }},
		{cfg.DingTalk.Enable, func() Result { return n.sendDingTalk(cfg.DingTalk, msg) }},
		{cfg.Feishu.Enable, func() Result { return n.sendFeishu(cfg.Feishu, msg) }},
		{cfg.ServerChan.Enable, func() Result { return n.sendServerChan(cfg.ServerChan, msg) }},
		{cfg.Bark.Enable, func() Result { return n.sendBark(cfg.Bark, msg) }},
		{cfg.Telegram.Enable, func() Result { return n.sendTelegram(cfg.Telegram, msg) }},
	}
	for _, ch := range channels {
		if !ch.enabled {
			continue
		}
		ch := ch
		wg.Add(1)
		go func() {
			defer wg.Done()
			record(ch.send())
		}()
	}
	for _, ch := range cfg.Channels {
		if !ch.Enable || strings.TrimSpace(ch.URL) == "" {
			continue
		}
		ch := ch
		wg.Add(1)
		go func() {
			defer wg.Done()
			record(n.sendWebhook(ch, msg))
		}()
	}
	wg.Wait()

	if len(results) == 0 {
		results = append(results, Result{Channel: "全部通道", OK: false, Error: "没有已启用的通知通道"})
	}
	for _, r := range results {
		if n.logger == nil {
			continue
		}
		if r.OK {
			n.logger.Log(logx.LevelInfo, "通知", fmt.Sprintf("%s 推送成功：%s", r.Channel, msg.Title))
		} else {
			n.logger.Log(logx.LevelWarn, "通知", fmt.Sprintf("%s 推送失败：%s", r.Channel, r.Error))
		}
	}
	return results
}

// ---------------------------------------------------------------------------
// OneBot v11 (QQ) channel
// ---------------------------------------------------------------------------

func (n *Notifier) sendQQ(cfg config.QQNotifierConfig, msg Message) Result {
	label := "QQ 机器人"
	scheme := "http"
	if strings.EqualFold(cfg.Protocol, "https") {
		scheme = "https"
	}
	host := strings.TrimSpace(cfg.Host)
	if host == "" {
		host = "127.0.0.1"
	}
	if strings.HasPrefix(host, "http://") || strings.HasPrefix(host, "https://") {
		u, err := url.Parse(host)
		if err != nil {
			return Result{Channel: label, OK: false, Error: "机器人地址不合法: " + err.Error()}
		}
		scheme = u.Scheme
		host = u.Hostname()
		if p := u.Port(); p != "" {
			if v, err := strconv.Atoi(p); err == nil {
				cfg.Port = v
			}
		}
	}
	if cfg.Port <= 0 {
		cfg.Port = 5700
	}
	base := fmt.Sprintf("%s://%s:%d", scheme, host, cfg.Port)
	text := msg.Text()

	sent := 0
	var lastErr string
	for _, gid := range cfg.GroupIDs {
		gid = strings.TrimSpace(gid)
		if gid == "" {
			continue
		}
		id, err := strconv.ParseInt(gid, 10, 64)
		if err != nil {
			lastErr = fmt.Sprintf("群号 %s 不是数字", gid)
			continue
		}
		payload := map[string]any{"group_id": id, "message": text, "auto_escape": false}
		if err := n.qqPost(base+"/send_group_msg", cfg.AccessToken, payload); err != nil {
			lastErr = err.Error()
			continue
		}
		sent++
	}
	if strings.TrimSpace(cfg.AdminQQ) != "" {
		id, err := strconv.ParseInt(strings.TrimSpace(cfg.AdminQQ), 10, 64)
		if err != nil {
			lastErr = "管理员 QQ 号不是数字"
		} else {
			payload := map[string]any{"user_id": id, "message": text, "auto_escape": false}
			if err := n.qqPost(base+"/send_private_msg", cfg.AccessToken, payload); err != nil {
				lastErr = err.Error()
			} else {
				sent++
			}
		}
	}
	if sent == 0 {
		if lastErr == "" {
			lastErr = "未填写群号或管理员 QQ 号"
		}
		return Result{Channel: label, OK: false, Error: lastErr}
	}
	return Result{Channel: label, OK: true}
}

func (n *Notifier) qqPost(endpoint, token string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(token) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("机器人返回状态 %d", resp.StatusCode)
	}
	var out struct {
		Status  string `json:"status"`
		RetCode int    `json:"retcode"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err == nil {
		if out.RetCode != 0 {
			return fmt.Errorf("机器人错误 %d: %s", out.RetCode, out.Message)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Generic webhook channel
// ---------------------------------------------------------------------------

func (n *Notifier) sendWebhook(ch config.NotifyChannel, msg Message) Result {
	name := ch.Name
	if strings.TrimSpace(name) == "" {
		name = "Webhook"
	}
	body, err := json.Marshal(map[string]any{
		"kind":   string(msg.Kind),
		"label":  KindLabel(msg.Kind),
		"title":  msg.Title,
		"body":   msg.Body,
		"target": msg.Target,
		"time":   msg.Time.Format(time.RFC3339),
		"text":   msg.Text(),
		"secret": ch.Secret,
	})
	if err != nil {
		return Result{Channel: name, OK: false, Error: err.Error()}
	}

	method := http.MethodPost
	contentType := "application/json"
	if strings.EqualFold(ch.Type, "get") {
		method = http.MethodGet
	}

	var reader *bytes.Reader
	if method == http.MethodGet {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, ch.URL, reader)
	if err != nil {
		return Result{Channel: name, OK: false, Error: err.Error()}
	}
	req.Header.Set("Content-Type", contentType)
	for k, v := range ch.Headers {
		req.Header.Set(k, v)
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return Result{Channel: name, OK: false, Error: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{Channel: name, OK: false, Error: fmt.Sprintf("返回状态 %d", resp.StatusCode)}
	}
	return Result{Channel: name, OK: true}
}
