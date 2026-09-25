package notify

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ycfrp/ycfrp/internal/config"
)

// 复现截图中的真实错误：11255 资源不存在
func TestQQErrorHintResourceGone(t *testing.T) {
	// 用户截图里的真实响应体
	body := `{"message":"请求的资源不存在(用户/群已注销)","code":11255,"err_code":40011028,"trace_id":"00000000000000000000000000000000"}`
	got := qqErrorHint(400, body)
	for _, want := range []string{"11255", "40011028", "请求的资源不存在", "处理建议", "与机器人建立会话", "属于别的机器人"} {
		if !strings.Contains(got, want) {
			t.Errorf("提示缺少 %q：\n%s", want, got)
		}
	}
	// 追踪号应保留，便于向平台排查
	if !strings.Contains(got, "00000000000000000000000000000000") {
		t.Error("应保留 trace_id")
	}
}

func TestQQErrorHintQuota(t *testing.T) {
	got := qqErrorHint(400, `{"message":"主动消息额度不足","code":40054012}`)
	if !strings.Contains(got, "额度") {
		t.Errorf("额度类错误应有对应建议：\n%s", got)
	}
}

func TestQQErrorHintUnauthorized(t *testing.T) {
	got := qqErrorHint(401, `{}`)
	if !strings.Contains(got, "鉴权失败") {
		t.Errorf("401 应提示鉴权失败：\n%s", got)
	}
}

func TestQQErrorHintKeepsRawWhenUnknown(t *testing.T) {
	got := qqErrorHint(500, `{"message":"服务器开小差了","code":99999}`)
	if !strings.Contains(got, "服务器开小差了") || !strings.Contains(got, "99999") {
		t.Errorf("未知错误应保留原始信息：\n%s", got)
	}
}

// 群与用户分别回报：一个成功一个失败时，整体算成功但带出失败原因
func TestQQOfficialPartialFailure(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// 换取凭证的接口
		if strings.HasSuffix(r.URL.Path, "/token") {
			w.Write([]byte(`{"access_token":"tk","expires_in":"7200"}`))
			return
		}
		calls = append(calls, r.URL.Path)
		if strings.Contains(r.URL.Path, "/users/") {
			// 用户推送失败（复现截图场景）
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"message":"请求的资源不存在(用户/群已注销)","code":11255,"err_code":40011028}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	origProd := qqOfficialProdBase
	origToken := qqOfficialTokenEndpoint
	// 把 token 与消息接口都指向假服务器
	qqOfficialProdBase = srv.URL
	qqOfficialTokenEndpoint = srv.URL + "/token"
	defer func() { qqOfficialProdBase = origProd; qqOfficialTokenEndpoint = origToken }()

	n := newTestNotifier()
	cfg := config.QQOfficialConfig{
		Enable: true, AppID: "app", AppSecret: "sec",
		GroupOpenIDs: "GROUP1234567890", UserOpenID: "USER0987654321",
	}
	res := n.sendQQOfficial(cfg, testMessage())
	if !res.OK {
		t.Fatalf("有一个目标成功时整体应算成功，实际: %+v", res)
	}
	if !strings.Contains(res.Error, "部分成功") {
		t.Errorf("应说明部分成功：%s", res.Error)
	}
	if !strings.Contains(res.Error, "处理建议") {
		t.Errorf("失败原因应带处理建议：%s", res.Error)
	}
	if len(calls) != 2 {
		t.Errorf("应尝试两个目标，实际 %d 次：%v", len(calls), calls)
	}
}

// 群与用户都失败时应整体失败，并汇总原因
func TestQQOfficialAllFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/token") {
			w.Write([]byte(`{"access_token":"tk","expires_in":"7200"}`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"message":"请求的资源不存在(用户/群已注销)","code":11255}`))
	}))
	defer srv.Close()

	origProd := qqOfficialProdBase
	origToken := qqOfficialTokenEndpoint
	qqOfficialProdBase = srv.URL
	qqOfficialTokenEndpoint = srv.URL + "/token"
	defer func() { qqOfficialProdBase = origProd; qqOfficialTokenEndpoint = origToken }()

	n := newTestNotifier()
	cfg := config.QQOfficialConfig{Enable: true, AppID: "a", AppSecret: "s", GroupOpenIDs: "G1"}
	res := n.sendQQOfficial(cfg, testMessage())
	if res.OK {
		t.Error("全部失败时应返回失败")
	}
	if !strings.Contains(res.Error, "全部推送失败") {
		t.Errorf("应说明全部失败：%s", res.Error)
	}
}

// 未填任何目标时应给出明确提示
func TestQQOfficialNoTarget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"tk","expires_in":"7200"}`))
	}))
	defer srv.Close()
	origToken := qqOfficialTokenEndpoint
	qqOfficialTokenEndpoint = srv.URL
	defer func() { qqOfficialTokenEndpoint = origToken }()

	n := newTestNotifier()
	res := n.sendQQOfficial(config.QQOfficialConfig{Enable: true, AppID: "a", AppSecret: "s"}, testMessage())
	if res.OK || !strings.Contains(res.Error, "无法确定推送目标") {
		t.Errorf("未填目标时应明确提示：%+v", res)
	}
}

func TestShortID(t *testing.T) {
	if got := shortID("00000000000000000000000000000003"); len(got) > 14 {
		t.Errorf("长 openid 应被截短，实际: %s", got)
	}
	if got := shortID("abc"); got != "abc" {
		t.Errorf("短 ID 应原样返回: %s", got)
	}
}

// 真实场景：用户把 A 面板（机器人甲）的 openid 填到 B 面板（机器人乙）里。
// openid 按机器人分别生成，平台只会回泛化的「资源不存在」，
// 面板应能点破真正原因，并列出本机器人可用的 openid。
func TestQQOfficialCrossBotOpenIDMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/token") {
			w.Write([]byte(`{"access_token":"tk","expires_in":"7200"}`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"message":"请求的资源不存在(用户/群已注销)","code":11255,"err_code":40011028}`))
	}))
	defer srv.Close()

	origProd := qqOfficialProdBase
	origToken := qqOfficialTokenEndpoint
	qqOfficialProdBase = srv.URL
	qqOfficialTokenEndpoint = srv.URL + "/token"
	defer func() { qqOfficialProdBase = origProd; qqOfficialTokenEndpoint = origToken }()

	n := newTestNotifier()
	cfg := config.QQOfficialConfig{
		Enable: true, AppID: "app", AppSecret: "sec",
		// 填的是别的机器人的 openid
		UserOpenID: "00000000000000000000000000000001",
		// 本机器人实际捕获到的是另一个
		Discovered: []config.DiscoveredOpenID{
			{UserOpenID: "00000000000000000000000000000002", Kind: "用户向机器人发起单聊"},
		},
	}
	res := n.sendQQOfficial(cfg, testMessage())
	if res.OK {
		t.Fatalf("应当推送失败，实际: %+v", res)
	}
	if !strings.Contains(res.Error, "不属于当前机器人") {
		t.Errorf("应指出 openid 归属不匹配：\n%s", res.Error)
	}
	// 应列出本机器人捕获到的 openid，供用户直接改用
	if !strings.Contains(res.Error, "000000") {
		t.Errorf("应列出本机器人可用的 openid：\n%s", res.Error)
	}
}

// 若本机器人一个 openid 都没捕获过，也应提示归属问题而不是让用户干猜。
func TestQQOfficialMismatchWithoutDiscovery(t *testing.T) {
	got := mismatchAdvice(nil, "00000000000000000000000000000001")
	if !strings.Contains(got, "按机器人分别生成") {
		t.Errorf("无捕获记录时也应提示 openid 不通用：\n%s", got)
	}
}

// openid 正是本机器人捕获过的，就不该再报归属问题。
func TestMismatchAdviceSilentWhenKnown(t *testing.T) {
	known := map[string]bool{"00000000000000000000000000000002": true}
	if got := mismatchAdvice(known, "00000000000000000000000000000002"); got != "" {
		t.Errorf("已知 openid 不应给归属提示：%s", got)
	}
}

var _ = json.Marshal