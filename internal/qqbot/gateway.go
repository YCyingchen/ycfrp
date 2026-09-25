package qqbot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// 网关的 opcode 定义，详见 QQ 机器人文档「事件订阅与通知」。
const (
	opDispatch     = 0
	opHeartbeat    = 1
	opIdentify     = 2
	opResume       = 6
	opReconnect    = 7
	opInvalidSess  = 9
	opHello        = 10
	opHeartbeatACK = 11
)

// intentGroupAndC2C 订阅群聊与单聊事件。
//
// 只订阅这一类（1<<25），因为面板需要的就是机器人被拉群、被 @、用户私聊
// 这三种事件——openid 全在里面。其余事件需要单独申请权限，而且与面板无关。
const intentGroupAndC2C = 1 << 25

// Event 是从网关收到的一条事件。
type Event struct {
	Type string          `json:"t"`
	Data json.RawMessage `json:"d"`
}

// OpenID 是一条事件里发现的群/用户标识。
type OpenID struct {
	// GroupOpenID 是群会话标识，非空表示来自群聊。
	GroupOpenID string
	// UserOpenID 是用户标识，来自单聊或群消息的发送者。
	UserOpenID string
	// MsgID 是被动消息 ID，可用于回复该条消息。
	MsgID string
	// Kind 描述这条标识的来源，便于在界面上说明。
	Kind string
	// At 是发现时间。
	At time.Time
}

// Handler 接收解析后的 openid。
type Handler func(OpenID)

// Client 维护一条到 QQ 网关的长连接。
type Client struct {
	appID     string
	appSecret string
	sandbox   bool
	proxy     string
	tokens    *TokenManager
	handler   Handler
	logf      func(string, ...any)

	mu      sync.Mutex
	running bool
	cancel  context.CancelFunc
	status  Status
}

// Status 描述当前连接状态，供面板展示。
type Status struct {
	Running     bool   `json:"running"`
	Connected   bool   `json:"connected"`
	SessionID   string `json:"sessionId"`
	LastEventAt string `json:"lastEventAt"`
	LastEvent   string `json:"lastEvent"`
	LastError   string `json:"lastError"`
	Events      int64  `json:"events"`
}

// NewClient 创建一个网关客户端。
func NewClient(tokens *TokenManager, handler Handler, logf func(string, ...any)) *Client {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Client{
		tokens:  tokens,
		handler: handler,
		logf:    logf,
	}
}

// Configure 更新连接参数。参数变化会重启连接。
func (c *Client) Configure(appID, appSecret string, sandbox bool, proxy string) {
	c.mu.Lock()
	changed := c.appID != appID || c.appSecret != appSecret ||
		c.sandbox != sandbox || c.proxy != proxy
	c.appID = appID
	c.appSecret = appSecret
	c.sandbox = sandbox
	c.proxy = proxy
	running := c.running
	c.mu.Unlock()

	if changed && running {
		c.logf("QQ 机器人配置已变化，重新连接网关")
		c.Stop()
		c.Start(context.Background())
	}
}

// Status 返回当前连接状态。
func (c *Client) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// Start 启动长连接维护循环。可重复调用，已运行时直接返回。
func (c *Client) Start(parent context.Context) {
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return
	}
	if strings.TrimSpace(c.appID) == "" || strings.TrimSpace(c.appSecret) == "" {
		c.status.Running = false
		c.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	c.cancel = cancel
	c.running = true
	c.status.Running = true
	c.mu.Unlock()

	go c.loop(ctx)
}

// Stop 停止长连接。
func (c *Client) Stop() {
	c.mu.Lock()
	cancel := c.cancel
	c.running = false
	c.status.Running = false
	c.status.Connected = false
	c.cancel = nil
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// loop 是维护循环：连上→收事件→断了重连，直到 context 取消。
func (c *Client) loop(ctx context.Context) {
	backoff := 3 * time.Second
	const maxBackoff = 2 * time.Minute

	for {
		if ctx.Err() != nil {
			return
		}
		err := c.session(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			c.setError(err.Error())
			// 网关主动要求重连（opReconnect）是 QQ 平台的正常心跳机制，
			// 与真正的网络故障区分开，避免日志里反复出现「连接中断」让人误以为是故障。
			if strings.Contains(err.Error(), "要求重连") {
				c.logf("QQ 网关按平台要求定时重连（正常心跳），%s 后重新连接", backoff)
			} else {
				c.logf("QQ 网关连接中断：%v，%s 后重连", err, backoff)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		// 连续失败时逐步退避，连上过就重置，避免断网期间疯狂重连。
		if err == nil {
			backoff = 3 * time.Second
		} else if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

// session 建立一次完整的连接会话：取凭证 → 拉网关地址 → 鉴权 → 心跳 → 收事件。
func (c *Client) session(ctx context.Context) error {
	c.mu.Lock()
	appID, secret, sandbox, proxy := c.appID, c.appSecret, c.sandbox, c.proxy
	c.mu.Unlock()

	token, err := c.tokens.Token(appID, secret, proxy)
	if err != nil {
		return err
	}
	gateway, err := c.gatewayURL(token, sandbox, proxy)
	if err != nil {
		return err
	}

	dialer := websocket.Dialer{HandshakeTimeout: 20 * time.Second}
	if p := strings.TrimSpace(proxy); p != "" {
		if pu, perr := url.Parse(p); perr == nil && pu.Host != "" {
			dialer.Proxy = http.ProxyURL(pu)
		}
	}
	conn, _, err := dialer.DialContext(ctx, gateway, nil)
	if err != nil {
		return fmt.Errorf("连接网关失败：%w", err)
	}
	defer conn.Close()

	var writeMu sync.Mutex
	writeJSON := func(v any) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteJSON(v)
	}

	// 等待 Hello，拿到心跳间隔。
	conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	var hello struct {
		Op int `json:"op"`
		D  struct {
			HeartbeatInterval int `json:"heartbeat_interval"`
		} `json:"d"`
	}
	if err := conn.ReadJSON(&hello); err != nil {
		return fmt.Errorf("等待网关握手失败：%w", err)
	}
	if hello.Op != opHello {
		return fmt.Errorf("网关握手返回了非预期消息（op=%d）", hello.Op)
	}
	interval := hello.D.HeartbeatInterval
	if interval <= 0 {
		interval = 45000
	}
	conn.SetReadDeadline(time.Time{})

	// 鉴权。
	if err := writeJSON(map[string]any{
		"op": opIdentify,
		"d": map[string]any{
			"token":   "QQBot " + token,
			"intents": intentGroupAndC2C,
			"properties": map[string]string{
				"$os": "linux", "$browser": "ycfrp", "$device": "ycfrp",
			},
		},
	}); err != nil {
		return fmt.Errorf("发送鉴权失败：%w", err)
	}

	// 心跳循环。
	hbCtx, hbCancel := context.WithCancel(ctx)
	defer hbCancel()
	go func() {
		ticker := time.NewTicker(time.Duration(interval) * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-ticker.C:
				if err := writeJSON(map[string]any{"op": opHeartbeat, "d": nil}); err != nil {
					return
				}
			}
		}
	}()

	c.setConnected(true)
	c.logf("QQ 机器人网关已连接，开始接收事件")

	for {
		var msg struct {
			Op int             `json:"op"`
			T  string          `json:"t"`
			D  json.RawMessage `json:"d"`
		}
		if err := conn.ReadJSON(&msg); err != nil {
			c.setConnected(false)
			return fmt.Errorf("读取网关消息失败：%w", err)
		}
		switch msg.Op {
		case opDispatch:
			c.handleDispatch(msg.T, msg.D)
		case opHeartbeatACK:
			// 正常回执，忽略。
		case opReconnect:
			c.setConnected(false)
			return fmt.Errorf("网关要求重连")
		case opInvalidSess:
			c.setConnected(false)
			// 鉴权信息失效，丢弃缓存的凭证以便下次重取。
			c.tokens.Invalidate(appID)
			return fmt.Errorf("会话鉴权失效，将重新获取凭证")
		}
	}
}

// gatewayURL 拉取网关接入地址。
func (c *Client) gatewayURL(token string, sandbox bool, proxy string) (string, error) {
	endpoint := BaseFor(sandbox) + "/gateway/bot"
	client := &http.Client{Timeout: 15 * time.Second}
	if p := strings.TrimSpace(proxy); p != "" {
		if pu, err := url.Parse(p); err == nil && pu.Host != "" {
			client = &http.Client{
				Timeout:   15 * time.Second,
				Transport: &http.Transport{Proxy: http.ProxyURL(pu)},
			}
		}
	}
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "QQBot "+token)
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("获取网关地址失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("获取网关地址失败：HTTP %d", resp.StatusCode)
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("解析网关地址失败：%w", err)
	}
	if out.URL == "" {
		return "", fmt.Errorf("网关地址为空")
	}
	return out.URL, nil
}

// handleDispatch 解析事件并提取 openid。
func (c *Client) handleDispatch(eventType string, data json.RawMessage) {
	c.mu.Lock()
	c.status.LastEvent = eventType
	c.status.LastEventAt = time.Now().Format("2006-01-02 15:04:05")
	c.status.Events++
	c.mu.Unlock()

	// 事件体里可能带 group_openid / user_openid（发送者）/ author.user_openid。
	var payload struct {
		GroupOpenID string `json:"group_openid"`
		UserOpenID  string `json:"user_openid"`
		MsgID       string `json:"id"`
		Author      struct {
			UserOpenID string `json:"user_openid"`
			UnionOpenID string `json:"union_openid"`
		} `json:"author"`
		// 机器人被拉群时，群的标识在 d.group_openid；单聊在 author.user_openid。
		OpMemberOpenID string `json:"op_member_openid"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return
	}

	userOpenID := strings.TrimSpace(payload.Author.UserOpenID)
	if userOpenID == "" {
		userOpenID = strings.TrimSpace(payload.UserOpenID)
	}

	kind := eventKindLabel(eventType)
	group := strings.TrimSpace(payload.GroupOpenID)
	if group == "" && userOpenID == "" {
		// 没有可用的标识，仅记录事件类型。
		c.logf("收到 QQ 机器人事件 %s，但未包含 openid", eventType)
		return
	}

	// READY 事件只带机器人自身信息，不作为 openid 来源。
	if strings.EqualFold(eventType, "READY") {
		return
	}

	if c.handler != nil {
		c.handler(OpenID{
			GroupOpenID: group,
			UserOpenID:  userOpenID,
			MsgID:       strings.TrimSpace(payload.MsgID),
			Kind:        kind,
			At:          time.Now(),
		})
	}
}

// eventKindLabel 把事件类型翻成中文说明。
func eventKindLabel(t string) string {
	switch t {
	case "GROUP_ADD_ROBOT":
		return "机器人被加入群聊"
	case "GROUP_DEL_ROBOT":
		return "机器人被移出群聊"
	case "GROUP_AT_MESSAGE_CREATE":
		return "群内有人 @ 机器人"
	case "C2C_MESSAGE_CREATE":
		return "用户向机器人发起单聊"
	case "FRIEND_ADD":
		return "用户添加机器人"
	default:
		return t
	}
}

func (c *Client) setConnected(v bool) {
	c.mu.Lock()
	c.status.Connected = v
	c.mu.Unlock()
}

func (c *Client) setError(msg string) {
	c.mu.Lock()
	c.status.LastError = msg
	c.status.Connected = false
	c.mu.Unlock()
}
