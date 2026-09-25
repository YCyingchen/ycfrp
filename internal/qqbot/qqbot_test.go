package qqbot

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestBaseFor(t *testing.T) {
	if BaseFor(false) != ProdBase {
		t.Errorf("正式环境地址不正确: %s", BaseFor(false))
	}
	if BaseFor(true) != SandboxBase {
		t.Errorf("沙箱环境地址不正确: %s", BaseFor(true))
	}
}

// 凭证有效期内的重复调用必须复用缓存，不能每次都请求平台。
func TestTokenManagerCaches(t *testing.T) {
	var hits int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"tk-1","expires_in":"7200"}`))
	}))
	defer srv.Close()

	orig := TokenEndpoint
	TokenEndpoint = srv.URL
	defer func() { TokenEndpoint = orig }()

	m := NewTokenManager()
	for i := 0; i < 3; i++ {
		tok, err := m.Token("app1", "sec1", "")
		if err != nil {
			t.Fatalf("第 %d 次取凭证失败: %v", i+1, err)
		}
		if tok != "tk-1" {
			t.Errorf("凭证不正确: %s", tok)
		}
	}
	mu.Lock()
	got := hits
	mu.Unlock()
	if got != 1 {
		t.Errorf("应只请求平台 1 次，实际 %d 次", got)
	}
}

// expires_in 为数字时也要能正确解析。
func TestTokenManagerNumericExpiresIn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"access_token":"tk-2","expires_in":3600}`))
	}))
	defer srv.Close()

	orig := TokenEndpoint
	TokenEndpoint = srv.URL
	defer func() { TokenEndpoint = orig }()

	m := NewTokenManager()
	if _, err := m.Token("app2", "sec2", ""); err != nil {
		t.Fatalf("取凭证失败: %v", err)
	}
	m.mu.Lock()
	c, ok := m.cache["app2"]
	m.mu.Unlock()
	if !ok {
		t.Fatal("未写入缓存")
	}
	// 3600 秒有效期，扣除 60 秒续期余量后应约为 59 分钟。
	left := time.Until(c.expires)
	if left < 55*time.Minute || left > 60*time.Minute {
		t.Errorf("有效期计算不正确，剩余 %v", left)
	}
}

// 平台失败时 HTTP 仍是 200，必须依据 body 里的 code 判定失败。
func TestTokenManagerSurfacesBizError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"code":100016,"message":"invalid appid or secret"}`))
	}))
	defer srv.Close()

	orig := TokenEndpoint
	TokenEndpoint = srv.URL
	defer func() { TokenEndpoint = orig }()

	m := NewTokenManager()
	_, err := m.Token("bad", "bad", "")
	if err == nil {
		t.Fatal("业务错误码必须被识别为失败")
	}
	// 应把错误码翻成中文提示。
	if got := err.Error(); !contains(got, "AppID 或 AppSecret 不正确") {
		t.Errorf("错误信息应中文化，实际: %s", got)
	}
}

func TestTokenManagerRequiresCreds(t *testing.T) {
	m := NewTokenManager()
	if _, err := m.Token("", "", ""); err == nil {
		t.Error("缺少 AppID/AppSecret 时应报错")
	}
}

// handleDispatch 应能从事件里正确提取群 openid 与发送者 openid。
func TestHandleDispatchExtractsOpenIDs(t *testing.T) {
	cases := []struct {
		name      string
		eventType string
		payload   string
		wantGroup string
		wantUser  string
		wantKind  string
	}{
		{
			name:      "群内@机器人",
			eventType: "GROUP_AT_MESSAGE_CREATE",
			payload:   `{"group_openid":"G123","id":"msg1","author":{"user_openid":"U456"}}`,
			wantGroup: "G123",
			wantUser:  "U456",
			wantKind:  "群内有人 @ 机器人",
		},
		{
			name:      "机器人被拉进群",
			eventType: "GROUP_ADD_ROBOT",
			payload:   `{"group_openid":"G789","op_member_openid":"U001"}`,
			wantGroup: "G789",
			wantKind:  "机器人被加入群聊",
		},
		{
			name:      "用户单聊",
			eventType: "C2C_MESSAGE_CREATE",
			payload:   `{"id":"msg9","author":{"user_openid":"U999"}}`,
			wantUser:  "U999",
			wantKind:  "用户向机器人发起单聊",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var captured *OpenID
			c := NewClient(NewTokenManager(), func(o OpenID) { captured = &o }, nil)
			c.handleDispatch(tc.eventType, json.RawMessage(tc.payload))
			if captured == nil {
				t.Fatal("未触发处理器")
			}
			if captured.GroupOpenID != tc.wantGroup {
				t.Errorf("群 openid=%q, 期望 %q", captured.GroupOpenID, tc.wantGroup)
			}
			if captured.UserOpenID != tc.wantUser {
				t.Errorf("用户 openid=%q, 期望 %q", captured.UserOpenID, tc.wantUser)
			}
			if captured.Kind != tc.wantKind {
				t.Errorf("事件说明=%q, 期望 %q", captured.Kind, tc.wantKind)
			}
		})
	}
}

// READY 事件只含机器人自身信息，不应被当作 openid 来源。
func TestHandleDispatchIgnoresReady(t *testing.T) {
	called := false
	c := NewClient(NewTokenManager(), func(o OpenID) { called = true }, nil)
	c.handleDispatch("READY", json.RawMessage(`{"user":{"id":"bot1"},"session_id":"s1"}`))
	if called {
		t.Error("READY 事件不应触发 openid 记录")
	}
}

// 没有 openid 的事件只记事件类型，不触发处理器。
func TestHandleDispatchWithoutOpenID(t *testing.T) {
	called := false
	var logged string
	c := NewClient(NewTokenManager(), func(o OpenID) { called = true },
		func(f string, a ...any) { logged = f })
	c.handleDispatch("GROUP_DEL_ROBOT", json.RawMessage(`{"op_member_openid":"U1"}`))
	if called {
		t.Error("无 openid 时不应触发处理器")
	}
	if logged == "" {
		t.Error("应记录一条日志")
	}
}

// 状态统计应随事件累加，供面板展示。
func TestStatusTracksEvents(t *testing.T) {
	c := NewClient(NewTokenManager(), nil, nil)
	c.handleDispatch("GROUP_AT_MESSAGE_CREATE", json.RawMessage(`{"group_openid":"G1"}`))
	c.handleDispatch("C2C_MESSAGE_CREATE", json.RawMessage(`{"author":{"user_openid":"U1"}}`))
	st := c.Status()
	if st.Events != 2 {
		t.Errorf("事件计数=%d, 期望 2", st.Events)
	}
	if st.LastEvent != "C2C_MESSAGE_CREATE" {
		t.Errorf("最近事件类型=%q", st.LastEvent)
	}
	if st.LastEventAt == "" {
		t.Error("应记录最近事件时间")
	}
}

// 未填写凭证时 Start 不应真的启动连接。
func TestStartWithoutCreds(t *testing.T) {
	c := NewClient(NewTokenManager(), nil, nil)
	c.Start(t.Context())
	if st := c.Status(); st.Running {
		t.Error("缺少凭证时不应进入运行状态")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		(len(s) > 0 && indexOf(s, sub) >= 0))
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
