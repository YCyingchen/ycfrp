package frp

import "time"

// Tunnel is a single frpc proxy entry managed by the panel.
type Tunnel struct {
	ID        string    `json:"id"`
	// InstanceID 指明这条隧道由哪个 frpc 实例承载。一台面板可以同时跑多个
	// frpc 实例，各连各的服务端，因此隧道必须归属到具体实例。
	InstanceID string  `json:"instanceId"`
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	Enabled   bool      `json:"enabled"`
	LocalIP   string    `json:"localIP"`
	LocalPort int       `json:"localPort"`
	RemotePort int      `json:"remotePort"`
	CustomDomains []string `json:"customDomains"`
	Subdomain string    `json:"subdomain"`
	SecretKey string    `json:"secretKey"`
	// Group 对 STCP / XTCP / SUDP 表示允许访问的用户列表，多个用逗号分隔。
	Group     string    `json:"group"`
	GroupKey  string    `json:"groupKey"`
	// 负载均衡：同一 GroupKey 下名称相同的多条隧道由服务端轮询分发。
	LoadBalanceGroup string `json:"loadBalanceGroup"`
	LoadBalanceKey   string `json:"loadBalanceKey"`
	UseEncryption  bool `json:"useEncryption"`
	UseCompression bool `json:"useCompression"`
	BandwidthLimit string `json:"bandwidthLimit"`
	ProxyProtocolVersion string `json:"proxyProtocolVersion"`
	HealthCheckType string `json:"healthCheckType"`
	HealthCheckURL  string `json:"healthCheckUrl"`
	HealthCheckInterval int `json:"healthCheckInterval"`
	HealthCheckTimeout  int `json:"healthCheckTimeout"`
	HealthCheckMaxFailed int `json:"healthCheckMaxFailed"`

	// HTTP / HTTPS / TCPMux 专用：路径路由、BasicAuth、请求头改写。
	Locations         []string          `json:"locations"`
	HTTPUser          string            `json:"httpUser"`
	HTTPPassword      string            `json:"httpPassword"`
	HostHeaderRewrite string            `json:"hostHeaderRewrite"`
	RequestHeaders    map[string]string `json:"requestHeaders"`
	ResponseHeaders   map[string]string `json:"responseHeaders"`
	RouteByHTTPUser   string            `json:"routeByHTTPUser"`

	Note      string    `json:"note"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`

	// External marks a row discovered from the server dashboard rather than
	// defined in this panel, which makes it read-only in the UI.
	External bool `json:"external"`

	// Runtime state, refreshed from the kernel.
	Status     string `json:"status"`
	RemoteAddr string `json:"remoteAddr"`
	Err        string `json:"err"`
	TodayIn    int64  `json:"todayIn"`
	TodayOut   int64  `json:"todayOut"`
	TotalIn    int64  `json:"totalIn"`
	TotalOut   int64  `json:"totalOut"`
	CurConns   int64  `json:"curConns"`
}

// EntityID implements store.Entity.
func (t Tunnel) EntityID() string { return t.ID }

// EntityCreatedAt implements store.Entity.
func (t Tunnel) EntityCreatedAt() time.Time { return t.CreatedAt }

// SetEntityMeta implements store.Entity.
func (t *Tunnel) SetEntityMeta(id string, created, updated time.Time) {
	t.ID = id
	if !created.IsZero() {
		t.CreatedAt = created
	}
	t.UpdatedAt = updated
}

// ProxyStatus describes a proxy reported by the frps dashboard API. Starting
// with frp v0.71.0 that endpoint emits camelCase fields and no longer repeats
// the local or remote address, so those are derived from Conf instead.
type ProxyStatus struct {
	Name            string    `json:"name"`
	Type            string    `json:"type"`
	Status          string    `json:"status"`
	User            string    `json:"user"`
	ClientID        string    `json:"clientID"`
	Conf            ProxyConf `json:"conf"`
	TodayTrafficIn  int64     `json:"todayTrafficIn"`
	TodayTrafficOut int64     `json:"todayTrafficOut"`
	CurConns        int64     `json:"curConns"`
	LastStartTime   string    `json:"lastStartTime"`
	LastCloseTime   string    `json:"lastCloseTime"`
}

// ProxyConf is the subset of a proxy definition echoed back by the dashboard.
type ProxyConf struct {
	Name          string     `json:"name"`
	Type          string     `json:"type"`
	LocalIP       string     `json:"localIP"`
	LocalPort     int        `json:"localPort"`
	RemotePort    int        `json:"remotePort"`
	CustomDomains []string   `json:"customDomains"`
	Subdomain     string     `json:"subdomain"`
	Plugin        PluginConf `json:"plugin"`
	// Metadatas carries the panel tunnel id under the ycfrpID key, which is
	// how a dashboard proxy is matched back to its local definition.
	Metadatas map[string]string `json:"metadatas"`
}

// PluginConf identifies a proxy plugin; only the type matters to the panel.
type PluginConf struct {
	Type string `json:"type"`
}

// ClientStatus describes a frpc client connected to the server.
type ClientStatus struct {
	Key              string `json:"key"`
	User             string `json:"user"`
	ClientID         string `json:"clientID"`
	Version          string `json:"version"`
	Hostname         string `json:"hostname"`
	IP               string `json:"ip"`
	Addr             string `json:"addr"`
	Online           bool   `json:"online"`
	FirstConnectedAt string `json:"firstConnectedAt"`
	LastConnectedAt  string `json:"lastConnectedAt"`
	ProxyCount       int    `json:"proxyCount"`
}

// ServerInfo mirrors the frps dashboard /api/serverinfo payload, which speaks
// JSON with camelCase keys.
type ServerInfo struct {
	Version               string           `json:"version"`
	BindPort              int              `json:"bindPort"`
	VhostHTTPPort         int              `json:"vhostHTTPPort"`
	VhostHTTPSPort        int              `json:"vhostHTTPSPort"`
	KCPBindPort           int              `json:"kcpBindPort"`
	QUICBindPort          int              `json:"quicBindPort"`
	SubdomainHost         string           `json:"subdomainHost"`
	TCPMuxHTTPConnectPort int              `json:"tcpmuxHTTPConnectPort"`
	MaxPoolCount          int64            `json:"maxPoolCount"`
	MaxPortsPerClient     int64            `json:"maxPortsPerClient"`
	HeartBeatTimeout      int64            `json:"heartbeatTimeout"`
	AllowPortsStr         string           `json:"allowPortsStr"`
	TotalTrafficIn        int64            `json:"totalTrafficIn"`
	TotalTrafficOut       int64            `json:"totalTrafficOut"`
	CurConns              int64            `json:"curConns"`
	ClientCounts          int64            `json:"clientCounts"`
	ProxyTypeCounts       map[string]int64 `json:"proxyTypeCount"`
}

// StatusResponse is the panel's consolidated runtime snapshot.
type StatusResponse struct {
	ServerRunning bool          `json:"serverRunning"`
	ClientRunning bool          `json:"clientRunning"`
	Server        *ServerInfo   `json:"server"`
	Tunnels       []Tunnel      `json:"tunnels"`
	Clients       []ClientStatus `json:"clients"`
	UpdatedAt     time.Time     `json:"updatedAt"`
}

// ProxyTypeLabel maps a raw frp proxy type to a Chinese label.
func ProxyTypeLabel(t string) string {
	switch t {
	case "tcp":
		return "TCP"
	case "udp":
		return "UDP"
	case "http":
		return "HTTP"
	case "https":
		return "HTTPS"
	case "tcpmux":
		return "TCPMux"
	case "stcp":
		return "STCP"
	case "xtcp":
		return "XTCP"
	case "sudp":
		return "SUDP"
	default:
		return t
	}
}

// ProxyPhaseLabel maps an frp proxy phase to a Chinese label. The frps
// dashboard reports online/offline while the frpc side reports the richer
// client phases, so both vocabularies are handled.
func ProxyPhaseLabel(phase string) string {
	switch phase {
	case "online":
		return "运行中"
	case "offline":
		return "已离线"
	case "running":
		return "运行中"
	case "start error":
		return "启动失败"
	case "waiting", "wait start":
		return "等待中"
	case "new":
		return "新建"
	case "check failed":
		return "检测失败"
	case "closed":
		return "已停止"
	case "not running":
		return "未启动"
	default:
		if phase == "" {
			return "未知"
		}
		return phase
	}
}
