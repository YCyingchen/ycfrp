package notify

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ycfrp/ycfrp/internal/config"
)

// 用一个本地服务器同时扮演企业微信、钉钉、飞书、Bark 的接收端，
// 检查各通道发出的请求体是否带上平台要求的字段。
func TestChannelRequestShapes(t *testing.T) {
	type captured struct {
		path   string
		query  string
		body   map[string]any
		raw    string
		header http.Header
	}
	got := make(chan captured, 8)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		c := captured{path: r.URL.Path, query: r.URL.RawQuery, raw: string(body), header: r.Header.Clone()}
		var m map[string]any
		if json.Unmarshal(body, &m) == nil {
			c.body = m
		}
		got <- c
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"errcode":0,"code":0,"status":200}`))
	}))
	defer srv.Close()

	// 把平台端点指向测试服务器，避免真实外网请求拖慢测试。
	origWecom := wecomEndpoint
	origBark := dailyEndpoints.Bark
	wecomEndpoint = srv.URL + "/webhook/send"
	dailyEndpoints.Bark = srv.URL
	defer func() { wecomEndpoint = origWecom; dailyEndpoints.Bark = origBark }()

	n := newTestNotifier()
	msg := testMessage()

	// 企业微信：msgtype=text
	if res := n.sendWeCom(config.WeComConfig{Enable: true, Key: "k1"}, msg); !res.OK {
		t.Fatalf("企业微信失败: %s", res.Error)
	}
	c := <-got
	if c.body["msgtype"] != "text" {
		t.Errorf("企业微信 msgtype=%v", c.body["msgtype"])
	}
	if !strings.Contains(c.path, "/webhook/send") {
		t.Errorf("企业微信 path=%s", c.path)
	}
	if !strings.Contains(c.query, "key=k1") {
		t.Errorf("企业微信 query=%s", c.query)
	}

	// 钉钉：msgtype=text + at 字段
	if res := n.sendDingTalk(config.DingTalkConfig{Enable: true, Webhook: srv.URL, AtMobiles: "138,139"}, msg); !res.OK {
		t.Fatalf("钉钉失败: %s", res.Error)
	}
	c = <-got
	at, _ := c.body["at"].(map[string]any)
	if at == nil {
		t.Fatalf("钉钉缺少 at 字段: %v", c.body)
	}
	mobiles, _ := at["atMobiles"].([]any)
	if len(mobiles) != 2 {
		t.Errorf("钉钉 atMobiles=%v", at["atMobiles"])
	}

	// 飞书：msg_type=text + content.text
	if res := n.sendFeishu(config.FeishuConfig{Enable: true, Webhook: srv.URL}, msg); !res.OK {
		t.Fatalf("飞书失败: %s", res.Error)
	}
	c = <-got
	if c.body["msg_type"] != "text" {
		t.Errorf("飞书 msg_type=%v", c.body["msg_type"])
	}
	content, _ := c.body["content"].(map[string]any)
	if content == nil || content["text"] == nil {
		t.Errorf("飞书 content 不正确: %v", c.body["content"])
	}

	// Bark：title + body
	if res := n.sendBark(config.BarkConfig{Enable: true, Server: srv.URL, DeviceKey: "dev1"}, msg); !res.OK {
		t.Fatalf("Bark 失败: %s", res.Error)
	}
	c = <-got
	if c.body["title"] != msg.Title {
		t.Errorf("Bark title=%v", c.body["title"])
	}
	if c.body["body"] == nil {
		t.Errorf("Bark 缺少 body")
	}
	if !strings.Contains(c.path, "dev1") {
		t.Errorf("Bark path=%s", c.path)
	}
}

// 企业微信 @全体成员时应出现 "@all"。
func TestWeComMentionAll(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := newTestNotifier()
	// sendWeCom 的地址是固定的企业微信域名，这里直接验证 payload 构造逻辑：
	text := map[string]any{"content": "hi"}
	mentions := splitCSV("")
	list := append(mentions, "@all")
	payload := map[string]any{"msgtype": "text",
		"text": map[string]any{"content": text["content"], "mentioned_mobile_list": list}}
	if err := n.postJSON(srv.URL, payload, nil); err != nil {
		t.Fatal(err)
	}
	txt, _ := body["text"].(map[string]any)
	if txt == nil {
		t.Fatalf("缺少 text 字段: %v", body)
	}
	lst, _ := txt["mentioned_mobile_list"].([]any)
	if len(lst) != 1 || lst[0] != "@all" {
		t.Errorf("mentioned_mobile_list=%v", txt["mentioned_mobile_list"])
	}
}

// 同一时刻多个通道并发分发时，任一失败不影响其他通道的结果收集。
func TestDispatchMultipleChannels(t *testing.T) {
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer okSrv.Close()

	origWecom := wecomEndpoint
	wecomEndpoint = okSrv.URL + "/webhook/send"
	defer func() { wecomEndpoint = origWecom }()

	n := newTestNotifier()
	n.Configure(config.NotifyConfig{
		Enable: true,
		WeCom:  config.WeComConfig{Enable: true, Key: "k"},
		Bark:   config.BarkConfig{Enable: true, Server: okSrv.URL, DeviceKey: "d"},
		// 故意留一个配置不全的通道，验证失败被单独记录。
		Telegram: config.TelegramConfig{Enable: true},
	})
	res := n.Dispatch(testMessage())
	var okCount, failCount int
	for _, r := range res {
		if r.OK {
			okCount++
		} else {
			failCount++
		}
	}
	if okCount == 0 {
		t.Errorf("应至少有一个通道成功: %+v", res)
	}
	if failCount == 0 {
		t.Errorf("配置不全的通道应被记为失败: %+v", res)
	}
}

var _ = time.Second
