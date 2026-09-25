package qqbot

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// 扫码绑定用到的官方接口地址（与官方 qqbot-connector SDK 一致）。
//
// 流程：创建绑定任务 → 让用户用手机 QQ 扫码 → 轮询结果 → 用任务密钥解密
// 平台返回的 AppSecret。全程只需出网访问 q.qq.com，不依赖任何第三方工具。
const (
	bindHostProd = "q.qq.com"
	bindHostTest = "test.q.qq.com"

	pathCreateBindTask  = "/lite/create_bind_task"
	pathPollBindResult  = "/lite/poll_bind_result"
	pathConnectPageTmpl = "/qqbot/openclaw/connect.html?task_id=%s&source=%s&_wv=2"
)

// BindStatus 是绑定任务的轮询状态，取值与官方 SDK 一致。
type BindStatus int

const (
	BindNone      BindStatus = 0
	BindPending   BindStatus = 1
	BindCompleted BindStatus = 2
	BindExpired   BindStatus = 3
)

// StatusLabel 返回状态的中文说明。
func (s BindStatus) StatusLabel() string {
	switch s {
	case BindNone:
		return "等待扫码"
	case BindPending:
		return "已扫码，等待确认"
	case BindCompleted:
		return "绑定成功"
	case BindExpired:
		return "二维码已过期"
	default:
		return "未知状态"
	}
}

// bindHost 抽成变量便于测试注入。
var bindHost = bindHostProd

// bindScheme 与 bindHost 配对，测试时改成 http 以便指向本地服务器。
var bindScheme = "https"

// bindBase 返回绑定接口的基础地址。
func bindBase() string {
	return bindScheme + "://" + bindHost
}

// BindTask 是一次扫码绑定的会话。
type BindTask struct {
	// TaskID 由平台分配，用于轮询与生成二维码地址。
	TaskID string `json:"taskId"`
	// Key 是本次任务的随机密钥，仅用于解密返回的 AppSecret，不回传给前端以外的用途。
	Key string `json:"-"`
	// QRURL 是用户扫码要打开的地址。
	QRURL string `json:"qrUrl"`
	// CreatedAt 用于判断是否已超时。
	CreatedAt time.Time `json:"createdAt"`
}

// BindResult 是一次轮询的结果。
type BindResult struct {
	Status    BindStatus `json:"status"`
	StatusTxt string     `json:"statusText"`
	AppID     string     `json:"appId"`
	AppSecret string     `json:"appSecret"`
	UserOpenID string    `json:"userOpenId"`
}

// IsTerminal 报告该结果是否已到终态，无需继续轮询。
func (r BindResult) IsTerminal() bool {
	return r.Status == BindCompleted || r.Status == BindExpired
}

// bindClient 是扫码绑定使用的 HTTP 客户端，超时比常规请求略长，
// 因为该接口偶尔响应较慢。
func bindClient() *http.Client {
	return &http.Client{Timeout: 15 * time.Second}
}

// CreateBindTask 向平台申请一个绑定任务，返回任务信息与扫码地址。
func CreateBindTask(source string) (*BindTask, error) {
	// 密钥为 32 字节随机数的 base64，恰好是 AES-256 的密钥长度。
	rawKey := make([]byte, 32)
	if _, err := rand.Read(rawKey); err != nil {
		return nil, fmt.Errorf("生成绑定密钥失败：%w", err)
	}
	key := base64.StdEncoding.EncodeToString(rawKey)

	endpoint := bindBase() + pathCreateBindTask
	payload := map[string]string{"key": key}
	var out struct {
		RetCode int    `json:"retcode"`
		Msg     string `json:"msg"`
		Data    struct {
			TaskID string `json:"task_id"`
		} `json:"data"`
	}
	if err := postJSON(endpoint, payload, &out); err != nil {
		return nil, err
	}
	if out.RetCode != 0 {
		return nil, fmt.Errorf("创建绑定任务失败：%s", orDefault(out.Msg, "平台返回错误码 "+strconv.Itoa(out.RetCode)))
	}
	if out.Data.TaskID == "" {
		return nil, fmt.Errorf("创建绑定任务失败：平台未返回任务号")
	}

	return &BindTask{
		TaskID:    out.Data.TaskID,
		Key:       key,
		QRURL:     buildConnectURL(out.Data.TaskID, source),
		CreatedAt: time.Now(),
	}, nil
}

// buildConnectURL 拼出扫码页地址。该地址由手机 QQ 打开后完成机器人绑定。
func buildConnectURL(taskID, source string) string {
	return bindBase() + fmt.Sprintf(pathConnectPageTmpl, url.QueryEscape(taskID), url.QueryEscape(source))
}

// PollBindResult 查询一次绑定结果。
func PollBindResult(taskID, key string) (BindResult, error) {
	endpoint := bindBase() + pathPollBindResult
	var out struct {
		RetCode int    `json:"retcode"`
		Msg     string `json:"msg"`
		Data    struct {
			Status           int    `json:"status"`
			BotAppID         any    `json:"bot_appid"`
			BotEncryptSecret string `json:"bot_encrypt_secret"`
			UserOpenID       string `json:"user_openid"`
			UserOpenidAlt    string `json:"userOpenid"`
		} `json:"data"`
	}
	if err := postJSON(endpoint, map[string]string{"task_id": taskID}, &out); err != nil {
		return BindResult{}, err
	}
	if out.RetCode != 0 {
		return BindResult{}, fmt.Errorf("查询绑定结果失败：%s", orDefault(out.Msg, "平台返回错误码 "+strconv.Itoa(out.RetCode)))
	}

	status := BindStatus(out.Data.Status)
	res := BindResult{
		Status:    status,
		StatusTxt: status.StatusLabel(),
		UserOpenID: firstNonEmptyStr(out.Data.UserOpenID, out.Data.UserOpenidAlt),
	}
	// bot_appid 在平台返回里可能是数字也可能是字符串，两种都要接。
	res.AppID = toStr(out.Data.BotAppID)

	if status != BindCompleted {
		return res, nil
	}

	secret, err := decryptSecret(out.Data.BotEncryptSecret, key)
	if err != nil {
		return res, err
	}
	res.AppSecret = secret
	return res, nil
}

// decryptSecret 解密平台返回的 AppSecret。
//
// 加密方式为 AES-256-GCM，密钥是创建任务时的随机 key 的 base64 解码；
// 密文（base64 解码后）的布局是 nonce(12) + ciphertext + tag(16)。
func decryptSecret(encrypted, keyB64 string) (string, error) {
	if strings.TrimSpace(encrypted) == "" {
		return "", fmt.Errorf("平台未返回密钥密文")
	}
	key, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil {
		return "", fmt.Errorf("绑定密钥格式不正确：%w", err)
	}
	data, err := base64.StdEncoding.DecodeString(encrypted)
	if err != nil {
		return "", fmt.Errorf("密文格式不正确：%w", err)
	}
	const nonceLen, tagLen = 12, 16
	if len(data) < nonceLen+tagLen {
		return "", fmt.Errorf("密文长度不足")
	}
	nonce := data[:nonceLen]
	tag := data[len(data)-tagLen:]
	ciphertext := data[nonceLen : len(data)-tagLen]

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("初始化解密失败：%w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("初始化 GCM 失败：%w", err)
	}
	// Go 的 GCM 期望密文与 tag 相连，这里按官方布局重新拼接。
	sealed := append(append([]byte{}, ciphertext...), tag...)
	plain, err := gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		return "", fmt.Errorf("解密密钥失败：%w", err)
	}
	return string(plain), nil
}

// postJSON 发送 JSON 请求并解析响应。
func postJSON(endpoint string, payload any, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := bindClient().Do(req)
	if err != nil {
		return fmt.Errorf("连接 QQ 开放平台失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("平台返回状态 %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("读取平台响应失败：%w", err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("解析平台响应失败：%w", err)
	}
	return nil
}

// toStr 把可能是数字或字符串的 JSON 值统一转成字符串。
func toStr(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		// JSON 数字统一按整数输出，避免出现 102xxxxx.0 这样的形式。
		return strconv.FormatInt(int64(x), 10)
	case json.Number:
		return x.String()
	default:
		return fmt.Sprint(x)
	}
}

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

func firstNonEmptyStr(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
