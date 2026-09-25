package logx

import (
	"regexp"
	"strings"
)

// The embedded frp kernel writes fixed English log lines shaped like
//
//	2026-09-23 12:00:00.000 [I] [client/service.go:123] try to connect to server...
//
// The kernel cannot be made to emit Chinese without forking third-party code,
// so the panel translates well-known messages here instead. Unmatched messages
// and the dynamic parameter values (IP, port, error text) are left as-is, and
// the original English line is always retained in Entry.Raw.

var (
	reKernelLevel  = regexp.MustCompile(`^\[[TDIWE]\]\s+`)
	reKernelCaller = regexp.MustCompile(`^\[[^\[\]]*\.go:[0-9]+\]\s+`)
)

type transRule struct {
	re   *regexp.Regexp
	repl string
}

var kernelTrans = []transRule{
	// frpc control-connection lifecycle
	{regexp.MustCompile(`^try to connect to server\.\.\.$`), "正在连接服务端..."},
	// token 不匹配的完整组合规则必须放在所有「xxx error: (.*)」通配规则之前，
	// 否则会被通配规则先吃掉、参数仍是英文。
	{regexp.MustCompile(`^connect to server error: token in login doesn't match token from configuration$`),
		"连接服务端失败：登录令牌与服务端配置不一致（请检查服务端与客户端的「认证令牌 token」是否相同）"},
	{regexp.MustCompile(`^register control error: token in login doesn't match token from configuration$`),
		"注册控制连接失败：登录令牌与服务端配置不一致（请检查服务端与客户端的「认证令牌 token」是否相同）"},
	{regexp.MustCompile(`^connect to server error: (.*)$`), "连接服务端失败: $1"},
	{regexp.MustCompile(`^login to server success, get run id \[(.*)\]$`), "登录服务端成功，运行 ID [$1]"},
	{regexp.MustCompile(`^login to server (failed|error)(.*)$`), "登录服务端失败$2"},
	{regexp.MustCompile(`^login to the server failed: (.*)\. With loginFailExit enabled, no additional retries will be attempted$`),
		"登录服务端失败: $1（已开启「登录失败即退出」，不再重试）"},
	{regexp.MustCompile(`^new control error: (.*)$`), "新建控制连接失败: $1"},
	{regexp.MustCompile(`^register control error: (.*)$`), "注册控制连接失败: $1"},
	{regexp.MustCompile(`^write login error response error: (.*)$`), "回写登录错误响应失败: $1"},
	{regexp.MustCompile(`^complete control login error: (.*)$`), "完成控制连接登录失败: $1"},
	{regexp.MustCompile(`^register visitor conn error: (.*)$`), "注册访问连接失败: $1"},
	{regexp.MustCompile(`^client login info: ip \[(.*)\] version \[(.*)\] hostname \[(.*)\] os \[(.*)\] arch \[(.*)\]$`),
		"客户端登录信息: IP [$1] 版本 [$2] 主机名 [$3] 系统 [$4] 架构 [$5]"},
	{regexp.MustCompile(`^create new controller error: (.*)$`), "创建控制器失败: $1"},
	{regexp.MustCompile(`^no client control found for run id \[(.*)\]$`), "未找到运行 ID [$1] 对应的客户端控制连接"},
	{regexp.MustCompile(`^invalid NewWorkConn with run id \[(.*)\]$`), "运行 ID [$1] 的新工作连接无效"},
	{regexp.MustCompile(`^start new connection to server error: (.*)$`), "建立到服务端的新连接失败: $1"},
	{regexp.MustCompile(`^error during NewWorkConn authentication: (.*)$`), "新工作连接认证出错: $1"},
	{regexp.MustCompile(`^work connection write to server error: (.*)$`), "工作连接写回服务端失败: $1"},
	{regexp.MustCompile(`^StartWorkConn contains error: (.*)$`), "启动工作连接出错: $1"},
	{regexp.MustCompile(`^dispatch NatHoleResp message to related proxy error$`), "分发打洞响应给相关隧道出错"},
	{regexp.MustCompile(`^pong message contains error: (.*)$`), "pong 消息包含错误: $1"},

	// token 认证错误（服务端与客户端 token 不一致时最常见）
	{regexp.MustCompile(`^token in login doesn't match token from configuration$`),
		"登录令牌与服务端配置不一致（请检查服务端与客户端的「认证令牌 token」是否填写相同）"},
	{regexp.MustCompile(`^token in heartbeat doesn't match token from configuration$`),
		"心跳令牌与服务端配置不一致"},
	{regexp.MustCompile(`^token in NewWorkConn doesn't match token from configuration$`),
		"工作连接令牌与服务端配置不一致"},
	{regexp.MustCompile(`^session shutdown$`), "会话已关闭"},

	// 网络连接底层错误
	{regexp.MustCompile(`^dial tcp ([^:]+:[0-9]+): connectex: No connection could be made because the target machine actively refused it\.$`),
		"无法连接 $1：目标机器主动拒绝了连接（服务端未启动或端口不对）"},
	{regexp.MustCompile(`^non-TLS connection received on a TlsOnly server$`),
		"收到非 TLS 连接，但服务端已强制 TLS（请确认客户端也开启了 TLS）"},

	// heartbeats
	{regexp.MustCompile(`^receive heartbeat from server$`), "收到服务端心跳"},
	{regexp.MustCompile(`^send heartbeat to server$`), "向服务端发送心跳"},
	{regexp.MustCompile(`^receive heartbeat$`), "收到心跳"},
	{regexp.MustCompile(`^error during ping authentication: (.*), skip sending ping message$`), "ping 认证出错: $1，已跳过发送"},
	{regexp.MustCompile(`^heartbeat timeout$`), "心跳超时"},

	// frpc proxy lifecycle
	{regexp.MustCompile(`^\[([^\]]*)\] start error: (.*)$`), "[$1] 启动失败: $2"},
	{regexp.MustCompile(`^\[([^\]]*)\] start proxy success$`), "[$1] 隧道启动成功"},
	{regexp.MustCompile(`^control message dispatcher exited$`), "控制消息分发器已退出"},
	{regexp.MustCompile(`^new work connection registered$`), "已注册新的工作连接"},
	{regexp.MustCompile(`^work connection pool is full, discarding$`), "工作连接池已满，丢弃连接"},
	{regexp.MustCompile(`^replaced by client \[(.*)\] \(control ID ([0-9]+)\)$`), "已被客户端 [$1] 替换（控制 ID $2）"},
	{regexp.MustCompile(`^panic error: (.*)$`), "发生 panic: $1"},
	{regexp.MustCompile(`^get work connection from pool$`), "从连接池获取工作连接"},
	{regexp.MustCompile(`^no work connections available, (.*)$`), "无可用工作连接: $1"},
	{regexp.MustCompile(`^client exit success$`), "客户端退出成功"},

	// frps proxy management
	{regexp.MustCompile(`^new proxy \[([^\]]*)\] type \[([^\]]*)\] error: (.*)$`), "新增隧道 [$1]（类型 $2）失败: $3"},
	{regexp.MustCompile(`^new proxy \[([^\]]*)\] type \[([^\]]*)\] success$`), "新增隧道 [$1]（类型 $2）成功"},
	{regexp.MustCompile(`^received invalid ping: (.*)$`), "收到无效 ping: $1"},
	{regexp.MustCompile(`^close proxy \[([^\]]*)\] success$`), "关闭隧道 [$1] 成功"},

	// frps HTTP 虚拟主机转发错误：请求的域名/路径没有匹配到任何隧道。
	// 关键词告警会原样推给用户，必须翻成可操作的中文。
	{regexp.MustCompile(`^do http proxy request \[host: (.*)\] error: no route found: (.*)$`),
		"HTTP 代理请求失败（主机 $1）：找不到对应的路由 [$2]。通常是该域名/路径没有配置对应的 HTTP 隧道，或隧道未启用"},

	// data-plane forwarding
	{regexp.MustCompile(`^connect to local service \[([^\]]*)\] error: (.*)$`), "连接本地服务 [$1] 失败: $2"},
	{regexp.MustCompile(`^write proxy protocol header to local conn error: (.*)$`), "向本地连接写入代理协议头失败: $1"},
	{regexp.MustCompile(`^proxy closing$`), "隧道正在关闭"},
	{regexp.MustCompile(`^failed to get work connection: (.*)$`), "获取工作连接失败: $1"},
	{regexp.MustCompile(`^get a new work connection: \[([^\]]*)\]$`), "获得新的工作连接: [$1]"},
	{regexp.MustCompile(`^met temporary error: (.*), sleep for (.*) \.\.\.$`), "遇到临时错误: $1，等待 $2 后重试"},
	{regexp.MustCompile(`^listener is closed: (.*)$`), "监听已关闭: $1"},
	{regexp.MustCompile(`^get a user connection \[([^\]]*)\]$`), "收到用户连接 [$1]"},
	{regexp.MustCompile(`^the user conn \[([^\]]*)\] was rejected, err:(.*)$`), "用户连接 [$1] 被拒绝，错误:$2"},
	{regexp.MustCompile(`^create encryption stream error: (.*)$`), "创建加密流失败: $1"},

	// frps service listeners
	{regexp.MustCompile(`^frps tcp listen on (.*)$`), "服务端 TCP 监听于 $1"},
	{regexp.MustCompile(`^frps kcp listen on udp (.*)$`), "服务端 KCP 监听于 UDP $1"},
	{regexp.MustCompile(`^frps quic listen on (.*)$`), "服务端 QUIC 监听于 $1"},
	{regexp.MustCompile(`^frps sshTunnelGateway listen on port ([0-9]+)$`), "服务端 SSH 隧道网关监听端口 $1"},
	{regexp.MustCompile(`^http service listen on (.*)$`), "HTTP 虚拟主机监听于 $1"},
	{regexp.MustCompile(`^https service listen on (.*)$`), "HTTPS 虚拟主机监听于 $1"},
	{regexp.MustCompile(`^dashboard listen on (.*)$`), "仪表盘监听于 $1"},
	{regexp.MustCompile(`^tcpmux httpconnect multiplexer listen on (.*), passthrough: (.*)$`), "TCPMux 多路复用监听于 $1，透传: $2"},
	{regexp.MustCompile(`^listener for incoming connections from client closed$`), "客户端连接监听已关闭"},
	{regexp.MustCompile(`^quic listener for incoming connections from client closed$`), "QUIC 客户端连接监听已关闭"},
	{regexp.MustCompile(`^dashboard server exit with error: (.*)$`), "仪表盘服务退出: $1"},
	{regexp.MustCompile(`^http: Server closed$`), "HTTP 服务已关闭"},
	{regexp.MustCompile(`^checkAndEnableTLSServerConnWithTimeout error: (.*)$`), "启用服务端 TLS 连接失败: $1"},

	// frpc visitor / misc
	{regexp.MustCompile(`^visitor listen on (.*)$`), "访问端监听于 $1"},
	{regexp.MustCompile(`^start proxy success$`), "隧道启动成功"},
	{regexp.MustCompile(`^proxy name \[([^\]]*)\] is already in use$`), "隧道名称 [$1] 已被占用"},
}

// translateKernel converts a cleaned kernel message to Chinese when a rule
// matches. The first matching rule wins; anything else is returned unchanged.
func translateKernel(msg string) string {
	for _, r := range kernelTrans {
		if r.re.MatchString(msg) {
			return r.re.ReplaceAllString(msg, r.repl)
		}
	}
	return msg
}

// cleanKernelMessage strips the kernel timestamp, level tag and caller prefix,
// then translates the remaining text to Chinese.
func cleanKernelMessage(line string) string {
	msg := strings.TrimSpace(line)
	// A full kernel line starts with the 23-char header "2006-01-02 15:04:05.000"
	// followed by a space. Strip the whole header, not just up to the first
	// space, since the timestamp itself contains a space between date and time.
	if len(msg) > 24 && msg[4] == '-' && msg[7] == '-' && msg[10] == ' ' && msg[19] == '.' && msg[23] == ' ' {
		msg = strings.TrimSpace(msg[24:])
	}
	msg = reKernelLevel.ReplaceAllString(msg, "")
	msg = reKernelCaller.ReplaceAllString(msg, "")
	return translateKernel(msg)
}
