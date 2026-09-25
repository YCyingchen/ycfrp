// Package qqbot 实现 QQ 开放平台机器人的接入能力：获取并缓存 access_token，
// 以及通过 WebSocket 网关订阅事件（机器人被拉群、被 @、用户私聊等）。
//
// 之所以要收事件：群与用户的 openid 由 QQ 平台按机器人分别生成，没有查询接口，
// 只能从平台推送的事件里取。面板据此自动识别 openid，省去手工抄写。
package qqbot

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// TokenEndpoint 是获取 access_token 的地址，抽成变量便于测试注入。
var TokenEndpoint = "https://api.bot.qq.com/app/getAppAccessToken"

// 沙箱环境与正式环境的开放接口地址。
// 抽成变量而非常量，便于单元测试指向本地假服务器。
var (
	// ProdBase 是正式环境的开放接口地址。
	ProdBase = "https://api.sgroup.qq.com"
	// SandboxBase 是沙箱环境的开放接口地址（用于开发调试）。
	SandboxBase = "https://sandbox.api.sgroup.qq.com"
)

// BaseFor 按沙箱开关返回应使用的接口地址。
func BaseFor(sandbox bool) string {
	if sandbox {
		return SandboxBase
	}
	return ProdBase
}

// cachedToken 是一份带过期时间的凭证。
type cachedToken struct {
	token   string
	expires time.Time
}

// TokenManager 缓存各 AppID 的 access_token。QQ 平台对获取凭证有频率限制
// （错误码 100001），因此同一个 AppID 必须复用缓存的凭证。
type TokenManager struct {
	mu     sync.Mutex
	cache  map[string]cachedToken
	client *http.Client
}

// NewTokenManager 创建凭证管理器。
func NewTokenManager() *TokenManager {
	return &TokenManager{
		cache:  make(map[string]cachedToken),
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

// Token 返回可用的 access_token，必要时自动获取或刷新。
//
// QQ 的凭证默认 2 小时有效，且在到期前 60 秒内请求会返回新凭证，
// 所以这里提前 60 秒续期，避免边界时刻用到过期凭证。
func (m *TokenManager) Token(appID, appSecret string, proxy string) (string, error) {
	appID = strings.TrimSpace(appID)
	appSecret = strings.TrimSpace(appSecret)
	if appID == "" || appSecret == "" {
		return "", fmt.Errorf("未填写 AppID 或 AppSecret")
	}

	m.mu.Lock()
	if c, ok := m.cache[appID]; ok && time.Now().Before(c.expires) {
		token := c.token
		m.mu.Unlock()
		return token, nil
	}
	m.mu.Unlock()

	token, ttl, err := m.fetch(appID, appSecret, proxy)
	if err != nil {
		return "", err
	}

	m.mu.Lock()
	m.cache[appID] = cachedToken{token: token, expires: time.Now().Add(ttl - 60*time.Second)}
	m.mu.Unlock()
	return token, nil
}

// Invalidate 丢弃某个 AppID 的缓存，用于凭证被平台拒绝时强制重取。
func (m *TokenManager) Invalidate(appID string) {
	m.mu.Lock()
	delete(m.cache, appID)
	m.mu.Unlock()
}

// fetch 向平台申请一份新凭证，返回凭证与其有效期。
func (m *TokenManager) fetch(appID, appSecret, proxy string) (string, time.Duration, error) {
	body, _ := json.Marshal(map[string]string{
		"appId":        appID,
		"clientSecret": appSecret,
	})

	client := m.client
	if p := strings.TrimSpace(proxy); p != "" {
		if pu, err := url.Parse(p); err == nil && pu.Host != "" {
			client = &http.Client{
				Timeout:   15 * time.Second,
				Transport: &http.Transport{Proxy: http.ProxyURL(pu)},
			}
		}
	}

	req, err := http.NewRequest(http.MethodPost, TokenEndpoint, strings.NewReader(string(body)))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("连接 QQ 开放平台失败：%w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", 0, fmt.Errorf("读取凭证响应失败：%w", err)
	}

	// 平台在失败时 HTTP 仍返回 200，必须看响应体里的 code。
	var out struct {
		AccessToken string          `json:"access_token"`
		ExpiresIn   json.RawMessage `json:"expires_in"`
		Code        int             `json:"code"`
		Message     string          `json:"message"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", 0, fmt.Errorf("解析凭证响应失败：%w", err)
	}
	if out.AccessToken == "" {
		msg := strings.TrimSpace(out.Message)
		if msg == "" {
			msg = strings.TrimSpace(string(data))
		}
		if out.Code != 0 {
			return "", 0, fmt.Errorf("获取凭证失败（%d）：%s", out.Code, zhTokenError(out.Code, msg))
		}
		return "", 0, fmt.Errorf("获取凭证失败：%s", msg)
	}

	// expires_in 有时是数字、有时是字符串，两种都要兼容。
	ttl := 7200 * time.Second
	raw := strings.Trim(string(out.ExpiresIn), `"`)
	if raw != "" && raw != "null" {
		if n, err := time.ParseDuration(raw + "s"); err == nil && n > 0 {
			ttl = n
		}
	}
	return out.AccessToken, ttl, nil
}

// zhTokenError 把平台的英文错误信息翻成中文提示，便于直接展示。
func zhTokenError(code int, msg string) string {
	switch code {
	case 100001:
		return "请求过于频繁，请稍后重试"
	case 100007:
		return "AppID 无效，或机器人状态异常（被封禁或已删除）"
	case 100016:
		return "AppID 或 AppSecret 不正确"
	case 10004:
		return "AppID 对应的机器人不存在"
	default:
		return msg
	}
}
