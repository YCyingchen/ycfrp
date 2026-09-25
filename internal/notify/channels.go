package notify

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ycfrp/ycfrp/internal/config"
)

// ---------------------------------------------------------------------------
// QQ 官方机器人（QQ 开放平台）
// ---------------------------------------------------------------------------

// QQ 开放平台的接口地址。抽成变量便于单元测试指向本地服务器。
var (
	qqOfficialProdBase    = "https://api.sgroup.qq.com"
	qqOfficialSandboxBase = "https://sandbox.api.sgroup.qq.com"
)

// 各平台的基础地址抽成变量，便于单元测试指向本地测试服务器。
var (
	qqOfficialTokenEndpoint = "https://bots.qq.com/app/getAppAccessToken"
	wecomEndpoint           = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send"
	dailyEndpoints          = struct{ ServerChan, Bark, Telegram string }{
		ServerChan: "https://sctapi.ftqq.com",
		Bark:       "https://api.day.app",
		Telegram:   "https://api.telegram.org",
	}
)

// sendQQOfficial 走 QQ 开放平台发送消息：先换取 access_token，再按群/单聊推送。
//
// 官方机器人对推送有较多限制，因此群与单聊分别尝试、分别回报结果，
// 方便用户看清是哪个目标失败、为什么失败。
func (n *Notifier) sendQQOfficial(cfg config.QQOfficialConfig, msg Message) Result {
	label := "QQ 官方机器人"
	appID := strings.TrimSpace(cfg.AppID)
	secret := strings.TrimSpace(cfg.AppSecret)
	if appID == "" || secret == "" {
		// 提示直接给出解决路径：面板支持扫码自动获取凭据。
		return Result{Channel: label, OK: false,
			Error: "未配置凭据，请在面板「通知」页点击「扫码绑定」用手机 QQ 扫码，或手动填写 AppID 与 AppSecret"}
	}

	token, err := n.qqOfficialToken(appID, secret)
	if err != nil {
		return Result{Channel: label, OK: false, Error: err.Error()}
	}

	base := qqOfficialProdBase
	if cfg.Sandbox {
		base = qqOfficialSandboxBase
	}
	text := msg.Text()
	msgID := strings.TrimSpace(cfg.MsgID)
	mode := "主动消息"
	if msgID != "" {
		mode = "被动回复"
	}

	// 本机器人从事件里捕获过的 openid。QQ 按机器人分别生成 openid，
	// 若用户填的是别的机器人的 openid，平台只会回「资源不存在」，很难自查，
	// 因此这里留一份名单用于比对诊断。
	known := discoveredIDs(cfg.Discovered)

	var (
		okTargets []string
		failures  []string
	)

	for _, gid := range splitCSV(cfg.GroupOpenIDs) {
		path := "/v2/groups/" + url.PathEscape(gid) + "/messages"
		payload := map[string]any{"content": text, "msg_type": 0}
		if msgID != "" {
			payload["msg_id"] = msgID
		}
		if err := n.qqOfficialPost(base+path, token, payload); err != nil {
			failures = append(failures, "群 "+shortID(gid)+"："+err.Error()+mismatchAdvice(known, gid))
			continue
		}
		okTargets = append(okTargets, "群 "+shortID(gid))
	}

	if uid := strings.TrimSpace(cfg.UserOpenID); uid != "" {
		path := "/v2/users/" + url.PathEscape(uid) + "/messages"
		payload := map[string]any{"content": text, "msg_type": 0}
		if msgID != "" {
			payload["msg_id"] = msgID
		}
		if err := n.qqOfficialPost(base+path, token, payload); err != nil {
			failures = append(failures, "用户 "+shortID(uid)+"："+err.Error()+mismatchAdvice(known, uid))
		} else {
			okTargets = append(okTargets, "用户 "+shortID(uid))
		}
	}

	if len(okTargets) == 0 {
		if len(failures) == 0 {
			return Result{Channel: label, OK: false,
				Error: "未填写群 openid 或用户 openid，无法确定推送目标"}
		}
		return Result{Channel: label, OK: false,
			Error: fmt.Sprintf("全部推送失败（%s）：%s", mode, strings.Join(failures, "；"))}
	}
	if len(failures) > 0 {
		// 部分成功也算成功，但把失败原因带出来便于排查。
		return Result{Channel: label, OK: true,
			Error: fmt.Sprintf("部分成功（%s）：%s；以下失败：%s",
				mode, strings.Join(okTargets, "、"), strings.Join(failures, "；"))}
	}
	return Result{Channel: label, OK: true,
		Error: fmt.Sprintf("已推送至 %s（%s）", strings.Join(okTargets, "、"), mode)}
}

// shortID 把长 openid 截短，避免错误信息里塞满几十位无意义字符。
func shortID(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= 10 {
		return s
	}
	return s[:6] + "…" + s[len(s)-4:]
}

// discoveredIDs 汇总本机器人从事件中捕获过的全部 openid。
func discoveredIDs(list []config.DiscoveredOpenID) map[string]bool {
	out := make(map[string]bool, len(list)*2)
	for _, d := range list {
		if g := strings.TrimSpace(d.GroupOpenID); g != "" {
			out[g] = true
		}
		if u := strings.TrimSpace(d.UserOpenID); u != "" {
			out[u] = true
		}
	}
	return out
}

// mismatchAdvice 判断被拒的 openid 是否属于「本机器人没见过的」目标。
//
// openid 由 QQ 按机器人分别生成：同一个群/用户，在不同机器人眼里值完全不同。
// 用户很容易从另一个面板（另一台机器人）复制过来，此时平台只会回
// 泛化的「资源不存在」，看不出真正原因。这里直接点破并给出可用目标。
func mismatchAdvice(known map[string]bool, id string) string {
	id = strings.TrimSpace(id)
	if id == "" || known[id] {
		return ""
	}
	if len(known) == 0 {
		return "\n注意：该 openid 不是本机器人捕获到的。openid 由 QQ 按机器人分别生成，" +
			"不同机器人之间不通用——请确认这个值来自当前 AppID 的机器人，" +
			"并在「自动发现」里点「填入」使用本机器人捕获到的 openid。"
	}
	cands := make([]string, 0, len(known))
	for k := range known {
		cands = append(cands, shortID(k))
	}
	sort.Strings(cands)
	return "\n注意：该 openid 不属于当前机器人（很可能是从别的机器人那边复制来的，" +
		"openid 按机器人分别生成、互不通用）。本机器人已捕获到：" +
		strings.Join(cands, "、") + "，请在下方「自动发现」里点「填入」。"
}

// qqErrorHint 把 QQ 开放平台的常见错误码翻成可操作的中文说明。
func qqErrorHint(status int, body string) string {
	code := jsonInt(body, "code")
	errCode := jsonInt(body, "err_code")
	msg := jsonString(body, "message")
	trace := jsonString(body, "trace_id")

	var advice string
	switch {
	case code == 11255 || errCode == 40011028:
		advice = "该 openid 对应的会话不存在。两种可能：" +
			"① 该 openid 尚未与机器人建立会话（群推送需先把机器人拉进群并在群里 @ 一次，单聊需对方先给机器人发过消息）；" +
			"② 该 openid 属于别的机器人——openid 由 QQ 按机器人分别生成，跨机器人不通用，" +
			"请用「自动发现」里本机器人捕获到的 openid。"
	case code == 40054012 || code == 40054013:
		advice = "主动消息额度已用尽。官方机器人主动推送有严格配额，" +
			"建议改用被动回复（填入收到的 msg_id），或仅使用群推送。"
	case code == 11244:
		advice = "消息被平台审核拦截，请检查内容是否包含敏感词。"
	case code == 40034005:
		advice = "机器人尚未上线。请到 QQ 开放平台确认机器人已通过审核并处于上线状态。"
	case code == 100007 || code == 100016 || code == 10004:
		advice = "凭据无效或机器人状态异常，建议重新扫码绑定。"
	case status == 401:
		advice = "鉴权失败，access_token 可能已过期，请重试或重新绑定。"
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "QQ 返回 %d", status)
	if code != 0 {
		fmt.Fprintf(&sb, "（错误码 %d", code)
		if errCode != 0 {
			fmt.Fprintf(&sb, "/%d", errCode)
		}
		sb.WriteString("）")
	}
	if msg != "" {
		sb.WriteString("：" + msg)
	}
	if advice != "" {
		sb.WriteString("\n处理建议：" + advice)
	}
	if trace != "" {
		sb.WriteString("\n追踪号：" + trace)
	}
	return sb.String()
}

// jsonInt 从 JSON 文本里取一个整数（容忍数字与字符串两种形式）。
func jsonInt(body, key string) int {
	var m map[string]any
	if json.Unmarshal([]byte(body), &m) != nil {
		return 0
	}
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case string:
		n, _ := strconv.Atoi(v)
		return n
	}
	return 0
}

// jsonString 从 JSON 文本里取一个字符串字段。
func jsonString(body, key string) string {
	var m map[string]any
	if json.Unmarshal([]byte(body), &m) != nil {
		return ""
	}
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}

// qqOfficialToken 用 AppID/AppSecret 换取 access_token。
func (n *Notifier) qqOfficialToken(appID, secret string) (string, error) {
	body, _ := json.Marshal(map[string]string{"appId": appID, "clientSecret": secret})
	req, err := http.NewRequest(http.MethodPost, qqOfficialTokenEndpoint, strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("获取 access_token 失败: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   string `json:"expires_in"`
		Message     string `json:"message"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("解析 access_token 响应失败: %w", err)
	}
	if out.AccessToken == "" {
		detail := out.Message
		if detail == "" {
			detail = string(data)
		}
		return "", fmt.Errorf("获取 access_token 失败: %s", strings.TrimSpace(detail))
	}
	return out.AccessToken, nil
}

func (n *Notifier) qqOfficialPost(endpoint, token string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "QQBot "+token)
	resp, err := n.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<18))
		return fmt.Errorf("%s", qqErrorHint(resp.StatusCode, strings.TrimSpace(string(data))))
	}
	return nil
}

// ---------------------------------------------------------------------------
// 企业微信群机器人
// ---------------------------------------------------------------------------

func (n *Notifier) sendWeCom(cfg config.WeComConfig, msg Message) Result {
	label := "企业微信"
	key := strings.TrimSpace(cfg.Key)
	if key == "" {
		return Result{Channel: label, OK: false, Error: "未填写机器人 Key"}
	}
	endpoint := wecomEndpoint + "?key=" + url.QueryEscape(key)

	text := msg.Text()
	payload := map[string]any{"msgtype": "text", "text": map[string]any{"content": text}}

	mentions := splitCSV(cfg.MentionList)
	if len(mentions) > 0 || cfg.MentionAll {
		textCfg := map[string]any{}
		if len(mentions) > 0 {
			textCfg["mentioned_mobile_list"] = mentions
		}
		if cfg.MentionAll {
			// 企业微信用 "@all" 表示全体成员。
			list := append(mentions, "@all")
			textCfg["mentioned_mobile_list"] = list
		}
		payload["text"] = map[string]any{"content": text, "mentioned_mobile_list": textCfg["mentioned_mobile_list"]}
	}

	if err := n.postJSON(endpoint, payload, nil); err != nil {
		return Result{Channel: label, OK: false, Error: err.Error()}
	}
	return Result{Channel: label, OK: true}
}

// ---------------------------------------------------------------------------
// 钉钉群机器人
// ---------------------------------------------------------------------------

func (n *Notifier) sendDingTalk(cfg config.DingTalkConfig, msg Message) Result {
	label := "钉钉"
	hook := strings.TrimSpace(cfg.Webhook)
	if hook == "" {
		return Result{Channel: label, OK: false, Error: "未填写 Webhook 地址"}
	}

	// 开启加签时把 timestamp 与 sign 拼到地址上。
	if secret := strings.TrimSpace(cfg.Secret); secret != "" {
		ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
		sign := dingTalkSign(ts, secret)
		sep := "?"
		if strings.Contains(hook, "?") {
			sep = "&"
		}
		hook = hook + sep + "timestamp=" + ts + "&sign=" + url.QueryEscape(sign)
	}

	text := msg.Text()
	if cfg.AtAll {
		text += "\n@所有人"
	}
	for _, m := range splitCSV(cfg.AtMobiles) {
		text += "\n@" + m
	}

	payload := map[string]any{
		"msgtype": "text",
		"text":    map[string]any{"content": text},
		"at": map[string]any{
			"atMobiles": splitCSV(cfg.AtMobiles),
			"isAtAll":   cfg.AtAll,
		},
	}
	if err := n.postJSON(hook, payload, nil); err != nil {
		return Result{Channel: label, OK: false, Error: err.Error()}
	}
	return Result{Channel: label, OK: true}
}

// dingTalkSign 按钉钉要求计算 HmacSHA256 签名：以 secret 为密钥、
// 对 "timestamp\nsecret" 做 HMAC 后再 base64。
func dingTalkSign(timestamp, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "\n" + secret))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// ---------------------------------------------------------------------------
// 飞书自定义机器人
// ---------------------------------------------------------------------------

func (n *Notifier) sendFeishu(cfg config.FeishuConfig, msg Message) Result {
	label := "飞书"
	hook := strings.TrimSpace(cfg.Webhook)
	if hook == "" {
		return Result{Channel: label, OK: false, Error: "未填写 Webhook 地址"}
	}
	payload := map[string]any{
		"msg_type": "text",
		"content":  map[string]any{"text": msg.Text()},
	}
	// 开启签名校验时，飞书要求在 body 里带上 timestamp 与 sign。
	if secret := strings.TrimSpace(cfg.Secret); secret != "" {
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		payload["timestamp"] = ts
		payload["sign"] = feishuSign(ts, secret)
	}
	if err := n.postJSON(hook, payload, nil); err != nil {
		return Result{Channel: label, OK: false, Error: err.Error()}
	}
	return Result{Channel: label, OK: true}
}

// feishuSign 按飞书要求计算签名：以 "timestamp\nsecret" 为密钥、
// 对空字符串做 HmacSHA256 后 base64。
func feishuSign(timestamp, secret string) string {
	var data []byte
	mac := hmac.New(sha256.New, []byte(timestamp+"\n"+secret))
	mac.Write(data)
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// ---------------------------------------------------------------------------
// Server 酱
// ---------------------------------------------------------------------------

func (n *Notifier) sendServerChan(cfg config.ServerChanConfig, msg Message) Result {
	label := "Server酱"
	key := strings.TrimSpace(cfg.SendKey)
	if key == "" {
		return Result{Channel: label, OK: false, Error: "未填写 SendKey"}
	}
	endpoint := dailyEndpoints.ServerChan + "/" + url.PathEscape(key) + ".send"
	form := url.Values{}
	form.Set("title", msg.Title)
	form.Set("desp", msg.Text())
	if ch := strings.TrimSpace(cfg.Channel); ch != "" {
		form.Set("channel", ch)
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return Result{Channel: label, OK: false, Error: err.Error()}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := n.client.Do(req)
	if err != nil {
		return Result{Channel: label, OK: false, Error: err.Error()}
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<18))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{Channel: label, OK: false, Error: fmt.Sprintf("返回状态 %d", resp.StatusCode)}
	}
	// Server 酱在 HTTP 200 时仍可能返回业务错误码。
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"message"`
	}
	if json.Unmarshal(data, &out) == nil && out.Code != 0 {
		return Result{Channel: label, OK: false, Error: fmt.Sprintf("推送失败 %d: %s", out.Code, out.Msg)}
	}
	return Result{Channel: label, OK: true}
}

// ---------------------------------------------------------------------------
// Bark（iOS）
// ---------------------------------------------------------------------------

func (n *Notifier) sendBark(cfg config.BarkConfig, msg Message) Result {
	label := "Bark"
	server := strings.TrimRight(strings.TrimSpace(cfg.Server), "/")
	if server == "" {
		server = dailyEndpoints.Bark
	}
	key := strings.TrimSpace(cfg.DeviceKey)
	if key == "" {
		return Result{Channel: label, OK: false, Error: "未填写设备 Key"}
	}
	endpoint := server + "/" + url.PathEscape(key)
	payload := map[string]any{
		"title": msg.Title,
		"body":  msg.Text(),
	}
	if s := strings.TrimSpace(cfg.Sound); s != "" {
		payload["sound"] = s
	}
	if g := strings.TrimSpace(cfg.Group); g != "" {
		payload["group"] = g
	}
	if err := n.postJSON(endpoint, payload, nil); err != nil {
		return Result{Channel: label, OK: false, Error: err.Error()}
	}
	return Result{Channel: label, OK: true}
}

// ---------------------------------------------------------------------------
// Telegram
// ---------------------------------------------------------------------------

func (n *Notifier) sendTelegram(cfg config.TelegramConfig, msg Message) Result {
	label := "Telegram"
	token := strings.TrimSpace(cfg.BotToken)
	chat := strings.TrimSpace(cfg.ChatID)
	if token == "" || chat == "" {
		return Result{Channel: label, OK: false, Error: "未填写 Bot Token 或 Chat ID"}
	}
	endpoint := dailyEndpoints.Telegram + "/bot" + token + "/sendMessage"
	payload := map[string]any{"chat_id": chat, "text": msg.Text()}

	// Telegram 在国内通常需要代理，这里允许为该通道单独指定。
	client := n.client
	if p := strings.TrimSpace(cfg.Proxy); p != "" {
		if pu, err := url.Parse(p); err == nil && pu.Host != "" {
			client = &http.Client{
				Timeout:   8 * time.Second,
				Transport: &http.Transport{Proxy: http.ProxyURL(pu)},
			}
		}
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return Result{Channel: label, OK: false, Error: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return Result{Channel: label, OK: false, Error: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<18))
		return Result{Channel: label, OK: false, Error: fmt.Sprintf("返回状态 %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))}
	}
	return Result{Channel: label, OK: true}
}

// ---------------------------------------------------------------------------
// 公共辅助
// ---------------------------------------------------------------------------

// postJSON 发送 JSON 请求并校验响应状态；channelLabel 为空时不解析业务码。
func (n *Notifier) postJSON(endpoint string, payload any, headers map[string]string) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<18))
		return fmt.Errorf("返回状态 %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return nil
}

// splitCSV 把逗号（或分号/空格）分隔的字符串切成列表，忽略空项。
func splitCSV(s string) []string {
	parts := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '，' || r == '；'
	})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
