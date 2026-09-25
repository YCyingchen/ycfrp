package qqbot

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// encryptSecret 按官方布局加密，用于构造测试密文：
// 密文 = base64( nonce(12) + ciphertext + tag(16) )
func encryptSecret(plain, keyB64 string) (string, error) {
	key, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nil, nonce, []byte(plain), nil)
	// Go 的 Seal 返回 ciphertext+tag，与官方布局一致。
	out := append(append([]byte{}, nonce...), sealed...)
	return base64.StdEncoding.EncodeToString(out), nil
}

func TestDecryptSecretRoundTrip(t *testing.T) {
	rawKey := make([]byte, 32)
	if _, err := rand.Read(rawKey); err != nil {
		t.Fatal(err)
	}
	keyB64 := base64.StdEncoding.EncodeToString(rawKey)
	const want = "DG5g3B4j9X2KOErG-abcdefghijklmn"

	enc, err := encryptSecret(want, keyB64)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decryptSecret(enc, keyB64)
	if err != nil {
		t.Fatalf("解密失败: %v", err)
	}
	if got != want {
		t.Errorf("解密结果=%q, 期望 %q", got, want)
	}
}

func TestDecryptSecretRejectsBadInput(t *testing.T) {
	rawKey := make([]byte, 32)
	rand.Read(rawKey)
	keyB64 := base64.StdEncoding.EncodeToString(rawKey)

	cases := []struct {
		name string
		enc  string
		key  string
	}{
		{"空密文", "", keyB64},
		{"密文非base64", "!!!not-base64!!!", keyB64},
		{"密钥非base64", "AAAA", "!!!bad!!!"},
		{"密文过短", base64.StdEncoding.EncodeToString([]byte("short")), keyB64},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := decryptSecret(c.enc, c.key); err == nil {
				t.Error("应当返回错误")
			}
		})
	}
}

// 用错误密钥解密必须失败（GCM 自带完整性校验）。
func TestDecryptSecretWrongKeyFails(t *testing.T) {
	k1 := make([]byte, 32)
	k2 := make([]byte, 32)
	rand.Read(k1)
	rand.Read(k2)
	enc, err := encryptSecret("secret-value", base64.StdEncoding.EncodeToString(k1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decryptSecret(enc, base64.StdEncoding.EncodeToString(k2)); err == nil {
		t.Error("错误密钥解密应当失败")
	}
}

func TestBuildConnectURL(t *testing.T) {
	u := buildConnectURL("task-123", "YCFRP")
	for _, want := range []string{
		"https://q.qq.com/qqbot/openclaw/connect.html",
		"task_id=task-123",
		"source=YCFRP",
		"_wv=2",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("地址缺少 %q: %s", want, u)
		}
	}
}

func TestToStr(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{"102xxxxx", "102xxxxx"},
		{float64(102345678), "102345678"},
		{nil, ""},
		{json.Number("999"), "999"},
	}
	for _, c := range cases {
		if got := toStr(c.in); got != c.want {
			t.Errorf("toStr(%v)=%q, 期望 %q", c.in, got, c.want)
		}
	}
}

func TestBindStatusLabel(t *testing.T) {
	cases := map[BindStatus]string{
		BindNone:      "等待扫码",
		BindPending:   "已扫码，等待确认",
		BindCompleted: "绑定成功",
		BindExpired:   "二维码已过期",
	}
	for s, want := range cases {
		if got := s.StatusLabel(); got != want {
			t.Errorf("状态 %d 标签=%q, 期望 %q", s, got, want)
		}
	}
}

// 端到端：用假平台跑通「创建任务 → 轮询 → 解密」全流程。
func TestBindFlowEndToEnd(t *testing.T) {
	var issuedKey string
	const wantSecret = "SECRET-FROM-QQ-PLATFORM"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case pathCreateBindTask:
			var body struct {
				Key string `json:"key"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			issuedKey = body.Key
			// 校验密钥长度：32 字节随机数的 base64
			raw, err := base64.StdEncoding.DecodeString(body.Key)
			if err != nil || len(raw) != 32 {
				t.Errorf("密钥不是 32 字节随机数的 base64: %v len=%d", err, len(raw))
			}
			w.Write([]byte(`{"retcode":0,"data":{"task_id":"TASK-ABC"}}`))
		case pathPollBindResult:
			var body struct {
				TaskID string `json:"task_id"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			if body.TaskID != "TASK-ABC" {
				t.Errorf("轮询携带的任务号错误: %s", body.TaskID)
			}
			enc, err := encryptSecret(wantSecret, issuedKey)
			if err != nil {
				t.Errorf("构造密文失败: %v", err)
			}
			// 模拟平台返回：appid 是数字、状态已完成
			resp, _ := json.Marshal(map[string]any{
				"retcode": 0,
				"data": map[string]any{
					"status":             2,
					"bot_appid":          102345678,
					"bot_encrypt_secret": enc,
					"user_openid":        "USER_OPENID_X",
				},
			})
			w.Write(resp)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	origHost, origScheme := bindHost, bindScheme
	bindHost = strings.TrimPrefix(srv.URL, "http://")
	bindScheme = "http"
	defer func() { bindHost, bindScheme = origHost, origScheme }()

	task, err := CreateBindTask("YCFRP")
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if task.TaskID != "TASK-ABC" {
		t.Errorf("任务号=%q", task.TaskID)
	}
	if !strings.Contains(task.QRURL, "task_id=TASK-ABC") {
		t.Errorf("扫码地址不正确: %s", task.QRURL)
	}

	res, err := PollBindResult(task.TaskID, task.Key)
	if err != nil {
		t.Fatalf("轮询失败: %v", err)
	}
	if res.Status != BindCompleted {
		t.Errorf("状态=%d, 期望已完成", res.Status)
	}
	if res.AppID != "102345678" {
		t.Errorf("AppID=%q（数字型 appid 应转成字符串）", res.AppID)
	}
	if res.AppSecret != wantSecret {
		t.Errorf("AppSecret=%q, 期望 %q", res.AppSecret, wantSecret)
	}
	if res.UserOpenID != "USER_OPENID_X" {
		t.Errorf("UserOpenID=%q", res.UserOpenID)
	}
	if !res.IsTerminal() {
		t.Error("已完成应视为终态")
	}
}

// 平台返回业务错误码时，应转成可读错误。
func TestCreateBindTaskBizError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"retcode":100001,"msg":"too many requests"}`))
	}))
	defer srv.Close()

	origHost, origScheme := bindHost, bindScheme
	bindHost = strings.TrimPrefix(srv.URL, "http://")
	bindScheme = "http"
	defer func() { bindHost, bindScheme = origHost, origScheme }()

	if _, err := CreateBindTask(""); err == nil {
		t.Error("业务错误码应被识别为失败")
	}
}

// 轮询到未完成状态时不带凭据。
func TestPollPendingHasNoSecret(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"retcode":0,"data":{"status":1}}`))
	}))
	defer srv.Close()

	origHost, origScheme := bindHost, bindScheme
	bindHost = strings.TrimPrefix(srv.URL, "http://")
	bindScheme = "http"
	defer func() { bindHost, bindScheme = origHost, origScheme }()

	res, err := PollBindResult("T", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != BindPending {
		t.Errorf("状态=%d", res.Status)
	}
	if res.AppSecret != "" || res.AppID != "" {
		t.Error("未完成时不应返回凭据")
	}
}

func TestQRCodeDataURL(t *testing.T) {
	got, err := QRCodeDataURL("https://q.qq.com/qqbot/openclaw/connect.html?task_id=x", 256)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "data:image/png;base64,") {
		t.Errorf("应返回 PNG 的 data URL，实际前缀: %.40s", got)
	}
	if len(got) < 200 {
		t.Errorf("二维码内容过短，可能生成失败: %d 字节", len(got))
	}
}
