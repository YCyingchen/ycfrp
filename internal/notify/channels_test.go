package notify

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ycfrp/ycfrp/internal/config"
	"github.com/ycfrp/ycfrp/internal/logx"
)

func TestSplitCSV(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"a,b,c", 3},
		{"a, b ,c", 3},
		{"a，b；c", 3},
		{"a\nb", 2},
		{"", 0},
		{"  ", 0},
		{"a,,b", 2},
	}
	for _, c := range cases {
		if got := len(splitCSV(c.in)); got != c.want {
			t.Errorf("splitCSV(%q) 长度=%d, 期望 %d", c.in, got, c.want)
		}
	}
}

func TestDingTalkSign(t *testing.T) {
	// 按钉钉文档：sign = base64(HmacSHA256(timestamp+"\n"+secret, secret))
	ts := "1700000000000"
	secret := "SEC0000test"
	want := func() string {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(ts + "\n" + secret))
		return base64.StdEncoding.EncodeToString(mac.Sum(nil))
	}()
	if got := dingTalkSign(ts, secret); got != want {
		t.Errorf("dingTalkSign 不匹配:\n got=%s\nwant=%s", got, want)
	}
}

func TestFeishuSign(t *testing.T) {
	// 按飞书文档：sign = base64(HmacSHA256("", key=timestamp+"\n"+secret))
	ts := "1700000000"
	secret := "feishu-secret"
	want := func() string {
		mac := hmac.New(sha256.New, []byte(ts+"\n"+secret))
		mac.Write(nil)
		return base64.StdEncoding.EncodeToString(mac.Sum(nil))
	}()
	if got := feishuSign(ts, secret); got != want {
		t.Errorf("feishuSign 不匹配:\n got=%s\nwant=%s", got, want)
	}
}

// newTestNotifier 造一个指向测试服务器的通知器。
func newTestNotifier() *Notifier {
	return &Notifier{
		logger:   logx.New(10),
		client:   &http.Client{Timeout: 5 * time.Second},
		cooldown: map[string]time.Time{},
	}
}

func testMessage() Message {
	return Message{
		Kind:   EventTest,
		Title:  "测试通知",
		Body:   "这是测试正文",
		Target: "单元测试",
		Time:   time.Now(),
	}
}

func TestSendWeComPayload(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer srv.Close()

	n := newTestNotifier()
	// 用测试服务器地址替换：企业微信通道把 key 拼在固定域名后，
	// 这里改为直接验证 postJSON 的 payload 形状。
	payload := map[string]any{
		"msgtype": "text",
		"text":    map[string]any{"content": testMessage().Text()},
	}
	if err := n.postJSON(srv.URL, payload, nil); err != nil {
		t.Fatal(err)
	}
	if got["msgtype"] != "text" {
		t.Errorf("msgtype=%v", got["msgtype"])
	}
}

func TestSendDingTalkAddsSign(t *testing.T) {
	var query string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"errcode":0}`))
	}))
	defer srv.Close()

	n := newTestNotifier()
	cfg := config.DingTalkConfig{Enable: true, Webhook: srv.URL, Secret: "s3cr3t", AtAll: true}
	res := n.sendDingTalk(cfg, testMessage())
	if !res.OK {
		t.Fatalf("钉钉推送失败: %s", res.Error)
	}
	if query == "" {
		t.Error("开启加签后 URL 应带 timestamp 与 sign")
	}
	at, _ := body["at"].(map[string]any)
	if at == nil || at["isAtAll"] != true {
		t.Errorf("at 字段不正确: %v", body["at"])
	}
}

func TestSendFeishuWithSecret(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"code":0}`))
	}))
	defer srv.Close()

	n := newTestNotifier()
	cfg := config.FeishuConfig{Enable: true, Webhook: srv.URL, Secret: "abc"}
	if res := n.sendFeishu(cfg, testMessage()); !res.OK {
		t.Fatalf("飞书推送失败: %s", res.Error)
	}
	if body["sign"] == nil || body["timestamp"] == nil {
		t.Errorf("开启签名后应带 sign 与 timestamp: %v", body)
	}
	if body["msg_type"] != "text" {
		t.Errorf("msg_type=%v", body["msg_type"])
	}
}

func TestSendBarkMissingKey(t *testing.T) {
	n := newTestNotifier()
	res := n.sendBark(config.BarkConfig{Enable: true}, testMessage())
	if res.OK {
		t.Error("缺少设备 Key 时应失败")
	}
}

func TestSendTelegramMissingConfig(t *testing.T) {
	n := newTestNotifier()
	res := n.sendTelegram(config.TelegramConfig{Enable: true}, testMessage())
	if res.OK {
		t.Error("缺少 Token/ChatID 时应失败")
	}
}

func TestQQOfficialMissingCreds(t *testing.T) {
	n := newTestNotifier()
	res := n.sendQQOfficial(config.QQOfficialConfig{Enable: true}, testMessage())
	if res.OK {
		t.Error("缺少 AppID/AppSecret 时应失败")
	}
}

func TestDispatchReportsDisabled(t *testing.T) {
	n := newTestNotifier()
	n.Configure(config.NotifyConfig{Enable: false})
	res := n.Dispatch(testMessage())
	if len(res) == 0 || res[0].OK {
		t.Errorf("全局未启用时应返回失败结果: %+v", res)
	}
}
