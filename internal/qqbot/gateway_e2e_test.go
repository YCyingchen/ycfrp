// 用假的 QQ 网关验证完整链路：取凭证 → 拉网关地址 → 鉴权 → 心跳 → 收事件 → 落库。
package qqbot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeGateway 同时扮演开放平台的两条 HTTP 接口与 WebSocket 网关。
type fakeGateway struct {
	srv      *httptest.Server
	upgrader websocket.Upgrader

	mu           sync.Mutex
	identifySeen bool
	helloSent    bool
	heartbeats   int
}

func newFakeGateway(t *testing.T) *fakeGateway {
	f := &fakeGateway{upgrader: websocket.Upgrader{}}
	mux := http.NewServeMux()

	// 换取凭证
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"tk-e2e","expires_in":"7200"}`))
	})
	// 拉网关地址：把 ws 地址指回本服务器
	mux.HandleFunc("/gateway/bot", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "QQBot ") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		ws := "ws" + strings.TrimPrefix(f.srv.URL, "http") + "/ws"
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"url": ws, "shards": 1})
	})
	// WebSocket 网关
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := f.upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		// 先发 Hello，声明心跳间隔（测试用 200ms，跑得快）
		conn.WriteJSON(map[string]any{"op": opHello,
			"d": map[string]any{"heartbeat_interval": 200}})
		f.mu.Lock()
		f.helloSent = true
		f.mu.Unlock()

		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var msg struct {
				Op int             `json:"op"`
				D  json.RawMessage `json:"d"`
			}
			if json.Unmarshal(data, &msg) != nil {
				continue
			}
			switch msg.Op {
			case opIdentify:
				// 校验 token 格式必须是 "QQBot <token>"
				var d struct {
					Token   string `json:"token"`
					Intents int    `json:"intents"`
				}
				json.Unmarshal(msg.D, &d)
				if d.Token != "QQBot tk-e2e" {
					conn.WriteJSON(map[string]any{"op": opInvalidSess})
					return
				}
				if d.Intents != intentGroupAndC2C {
					conn.WriteJSON(map[string]any{"op": opInvalidSess})
					return
				}
				f.mu.Lock()
				f.identifySeen = true
				f.mu.Unlock()

				// 鉴权通过后推 READY，再推一条真实的群 @ 事件
				conn.WriteJSON(map[string]any{"op": opDispatch, "s": 1, "t": "READY",
					"d": map[string]any{"session_id": "sess-1"}})
				conn.WriteJSON(map[string]any{"op": opDispatch, "s": 2, "t": "GROUP_AT_MESSAGE_CREATE",
					"d": map[string]any{
						"group_openid": "GROUP_OPENID_E2E",
						"id":           "MSG_E2E",
						"author":       map[string]any{"user_openid": "USER_OPENID_E2E"},
					}})
			case opHeartbeat:
				f.mu.Lock()
				f.heartbeats++
				f.mu.Unlock()
				conn.WriteJSON(map[string]any{"op": opHeartbeatACK})
			}
		}
	})

	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGateway) stats() (bool, bool, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.identifySeen, f.helloSent, f.heartbeats
}

// 端到端：客户端应完成鉴权并收到群 openid。
func TestEndToEndReceivesOpenID(t *testing.T) {
	f := newFakeGateway(t)
	base := f.srv.URL

	// 把平台的三个端点都指向假服务器。
	origToken, origProd := TokenEndpoint, ProdBase
	TokenEndpoint = base + "/token"
	ProdBase = base
	defer func() { TokenEndpoint, ProdBase = origToken, origProd }()

	got := make(chan OpenID, 4)
	c := NewClient(NewTokenManager(), func(o OpenID) { got <- o }, nil)
	c.Configure("app-e2e", "sec-e2e", false, "")
	c.Start(context.Background())
	defer c.Stop()

	select {
	case ev := <-got:
		if ev.GroupOpenID != "GROUP_OPENID_E2E" {
			t.Errorf("群 openid=%q", ev.GroupOpenID)
		}
		if ev.UserOpenID != "USER_OPENID_E2E" {
			t.Errorf("用户 openid=%q", ev.UserOpenID)
		}
		if ev.MsgID != "MSG_E2E" {
			t.Errorf("消息 ID=%q", ev.MsgID)
		}
		if ev.Kind != "群内有人 @ 机器人" {
			t.Errorf("事件说明=%q", ev.Kind)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("超时未收到 openid 事件")
	}

	// 鉴权应已发生，心跳也应跑过至少一轮。
	time.Sleep(600 * time.Millisecond)
	identify, hello, beats := f.stats()
	if !hello {
		t.Error("网关未发出 Hello")
	}
	if !identify {
		t.Error("客户端未发送合法鉴权")
	}
	if beats == 0 {
		t.Error("未收到心跳")
	}
	st := c.Status()
	if !st.Connected {
		t.Error("状态应显示已连接")
	}
	if st.Events < 2 {
		t.Errorf("事件计数=%d, 期望至少 2（READY + 群消息）", st.Events)
	}
}

// 网关返回无效会话时，客户端应识别为鉴权失败、不谎报已连接，并且不推送事件。
func TestEndToEndRejectsBadToken(t *testing.T) {
	upgrader := websocket.Upgrader{}
	badTokenHit := make(chan struct{}, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/token"):
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"access_token":"tk-bad","expires_in":"7200"}`))
		case strings.HasSuffix(r.URL.Path, "/gateway/bot"):
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"url": "ws://" + r.Host + "/ws"})
		case strings.HasSuffix(r.URL.Path, "/ws"):
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			// 发出 Hello 后，任何鉴权都直接判为无效会话。
			conn.WriteJSON(map[string]any{"op": opHello, "d": map[string]any{"heartbeat_interval": 1000}})
			for {
				_, data, err := conn.ReadMessage()
				if err != nil {
					return
				}
				var msg struct {
					Op int `json:"op"`
				}
				if json.Unmarshal(data, &msg) != nil {
					continue
				}
				if msg.Op == opIdentify {
					select {
					case badTokenHit <- struct{}{}:
					default:
					}
					conn.WriteJSON(map[string]any{"op": opInvalidSess})
					return
				}
			}
		}
	}))
	defer srv.Close()

	origToken, origProd := TokenEndpoint, ProdBase
	TokenEndpoint = srv.URL + "/token"
	ProdBase = srv.URL
	defer func() { TokenEndpoint, ProdBase = origToken, origProd }()

	got := make(chan OpenID, 1)
	c := NewClient(NewTokenManager(), func(o OpenID) { got <- o }, nil)
	c.Configure("app", "sec", false, "")
	c.Start(context.Background())
	defer c.Stop()

	// 客户端确实发出了鉴权。
	select {
	case <-badTokenHit:
	case <-time.After(10 * time.Second):
		t.Fatal("客户端未发出鉴权")
	}

	// 鉴权被拒后不应推送任何事件。
	select {
	case <-got:
		t.Error("鉴权失败时不应收到事件")
	case <-time.After(3 * time.Second):
	}

	if st := c.Status(); st.Connected {
		t.Error("鉴权失败后不应显示已连接")
	}
	if st := c.Status(); st.LastError == "" {
		t.Error("应记录错误原因")
	}
}
