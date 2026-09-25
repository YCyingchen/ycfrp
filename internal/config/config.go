package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ycfrp/ycfrp/internal/version"
)

const (
	DefaultPort     = 38080
	DefaultUser     = "admin"
	DefaultPassword = "admin"
)

// FRPSConfig holds the embedded frps server settings.
type FRPSConfig struct {
	Enable            bool   `json:"enable"`
	BindPort          int    `json:"bindPort"`
	KCPBindPort       int    `json:"kcpBindPort"`
	QUICBindPort      int    `json:"quicBindPort"`
	DashboardPort     int    `json:"dashboardPort"`
	DashboardUser     string `json:"dashboardUser"`
	DashboardPwd      string `json:"dashboardPwd"`
	VhostHTTPPort     int    `json:"vhostHttpPort"`
	VhostHTTPSPort    int    `json:"vhostHttpsPort"`
	Token             string `json:"token"`
	SubdomainHost     string `json:"subdomainHost"`
	MaxPortsPerClient int    `json:"maxPortsPerClient"`
	AllowPortsStart   int    `json:"allowPortsStart"`
	AllowPortsEnd     int    `json:"allowPortsEnd"`
	LogLevel          string `json:"logLevel"`
	LogMaxDays        int    `json:"logMaxDays"`
	BindAddr          string `json:"bindAddr"`
	TransportTLS      bool   `json:"transportTLS"`
}

// FRPCConfig holds the embedded frpc client settings. The client side is
// driven by individual tunnel entries, so only connection level options live
// here.
type FRPCConfig struct {
	Enable         bool   `json:"enable"`
	ServerAddr     string `json:"serverAddr"`
	ServerPort     int    `json:"serverPort"`
	Token          string `json:"token"`
	User           string `json:"user"`
	LoginFailExit  bool   `json:"loginFailExit"`
	LogLevel       string `json:"logLevel"`
	LogMaxDays     int    `json:"logMaxDays"`
	TransportTLS   bool   `json:"transportTLS"`
	TransportProto string `json:"transportProtocol"`
	// UseEncryption / UseCompression 是本实例新建隧道时的默认值。
	// 它们不是 frpc 连接级配置（frp 内核没有客户端级加密/压缩），
	// 而是面板侧的模板：新建隧道时预填隧道弹窗里的「加密传输 / 压缩传输」开关。
	UseEncryption  bool `json:"useEncryption"`
	UseCompression bool `json:"useCompression"`
}

// QQNotifierConfig configures the OneBot v11 QQ bot notification channel.
type QQNotifierConfig struct {
	Enable     bool     `json:"enable"`
	Protocol   string   `json:"protocol"`
	Host       string   `json:"host"`
	Port       int      `json:"port"`
	AccessToken string  `json:"accessToken"`
	AdminQQ    string   `json:"adminQQ"`
	GroupIDs   []string `json:"groupIds"`
	PushEnable bool     `json:"pushEnabled"`
}

// QQOfficialConfig 配置 QQ 官方机器人（QQ 开放平台）通道。
//
// 与 OneBot 不同，官方机器人走开放平台鉴权：先用 AppID/Secret 换
// access_token，再以 Bot 身份往群或单聊发消息。
type QQOfficialConfig struct {
	Enable        bool   `json:"enable"`
	AppID         string `json:"appId"`
	AppSecret     string `json:"appSecret"`
	// Sandbox 为真时使用沙箱环境地址（用于开发调试）。
	Sandbox       bool   `json:"sandbox"`
	// GroupOpenID 是官方机器人发送群消息所需的群 openid，多个用逗号分隔。
	GroupOpenIDs  string `json:"groupOpenIds"`
	// UserOpenID 用于单聊推送（可选）。
	UserOpenID    string `json:"userOpenId"`
	// MsgID 用于被动消息回复，留空则按主动消息发送。
	MsgID         string `json:"msgId"`
	// Proxy 供面板访问 QQ 开放平台时使用（外网受限时填写）。
	Proxy         string `json:"proxy"`
	// Listen 为真时面板会在后台接收 QQ 推送的事件，用于自动发现 openid。
	Listen        bool   `json:"listen"`
	// Discovered 记录从事件中自动发现的 openid，供界面选择填入。
	Discovered    []DiscoveredOpenID `json:"discovered"`
}

// DiscoveredOpenID 是从 QQ 事件中识别到的一条群/用户标识。
type DiscoveredOpenID struct {
	GroupOpenID string `json:"groupOpenId"`
	UserOpenID  string `json:"userOpenId"`
	Kind        string `json:"kind"`
	MsgID       string `json:"msgId"`
	At          string `json:"at"`
}

// WeComConfig 配置企业微信群机器人通道。
type WeComConfig struct {
	Enable bool   `json:"enable"`
	Key    string `json:"key"`
	// MentionList 是被 @ 的手机号列表，多个用逗号分隔（企业微信要求手机号）。
	MentionList string `json:"mentionList"`
	// MentionAll 为真时 @全体成员。
	MentionAll bool `json:"mentionAll"`
}

// DingTalkConfig 配置钉钉群机器人通道。
type DingTalkConfig struct {
	Enable bool   `json:"enable"`
	Webhook string `json:"webhook"`
	Secret string `json:"secret"`
	// AtMobiles 是被 @ 的手机号列表，多个用逗号分隔。
	AtMobiles string `json:"atMobiles"`
	AtAll     bool   `json:"atAll"`
}

// FeishuConfig 配置飞书自定义机器人通道。
type FeishuConfig struct {
	Enable  bool   `json:"enable"`
	Webhook string `json:"webhook"`
	// Secret 用于开启「签名校验」时的加签。
	Secret string `json:"secret"`
}

// ServerChanConfig 配置 Server 酱（sct.ftqq.com）推送通道。
type ServerChanConfig struct {
	Enable bool   `json:"enable"`
	SendKey string `json:"sendKey"`
	// Channel 是 Server 酱的渠道编号，留空使用默认。
	Channel string `json:"channel"`
}

// BarkConfig 配置 Bark（iOS 推送）通道。
type BarkConfig struct {
	Enable bool   `json:"enable"`
	Server string `json:"server"`
	DeviceKey string `json:"deviceKey"`
	// Sound 自定义提示音（可选）。
	Sound string `json:"sound"`
	// Group 通知分组（可选）。
	Group string `json:"group"`
}

// TelegramConfig 配置 Telegram 机器人通道。
type TelegramConfig struct {
	Enable  bool   `json:"enable"`
	BotToken string `json:"botToken"`
	ChatID  string `json:"chatId"`
	// Proxy 为该通道单独指定代理（外网访问受限时使用）。
	Proxy   string `json:"proxy"`
}

// NotifyChannel is a generic webhook style notification target.
type NotifyChannel struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	Type    string            `json:"type"`
	Enable  bool              `json:"enable"`
	URL     string            `json:"url"`
	Secret  string            `json:"secret"`
	Headers map[string]string `json:"headers"`
}

// NotifyConfig controls the notification dispatch behaviour.
type NotifyConfig struct {
	Enable         bool            `json:"enable"`
	QQ             QQNotifierConfig `json:"qq"`
	QQOfficial     QQOfficialConfig `json:"qqOfficial"`
	WeCom          WeComConfig      `json:"wecom"`
	DingTalk       DingTalkConfig   `json:"dingtalk"`
	Feishu         FeishuConfig     `json:"feishu"`
	ServerChan     ServerChanConfig `json:"serverChan"`
	Bark           BarkConfig       `json:"bark"`
	Telegram       TelegramConfig   `json:"telegram"`
	Channels       []NotifyChannel `json:"channels"`
	MonitorErrors  bool            `json:"monitorErrors"`
	MonitorOffline bool            `json:"monitorOffline"`
	MonitorTraffic bool            `json:"monitorTraffic"`
	TrafficLimitGB float64         `json:"trafficLimitGB"`
	Keywords       []string        `json:"keywords"`
	QuietMinutes   int             `json:"quietMinutes"`
}

// UIConfig stores the panel appearance preferences.
type UIConfig struct {
	Theme            string   `json:"theme"`
	AccentColor      string   `json:"accentColor"`
	Wallpaper        string   `json:"wallpaper"`
	WallpaperURL     string   `json:"wallpaperUrl"`
	WallpaperOpacity float64  `json:"wallpaperOpacity"`
	SiteName         string   `json:"siteName"`
	SiteLogo         string   `json:"siteLogo"`
	SiteFooter       string   `json:"siteFooter"`
	GlassEffect      bool     `json:"glassEffect"`
	// NavLayout 控制主导航的布局：top 上顶横放、left 左垂直、right 右垂直。
	NavLayout        string   `json:"navLayout"`
	// UIStyle 是界面视觉样式：default / crystal / retro / neon。
	UIStyle          string   `json:"uiStyle"`
	OnlineWallpapers []string `json:"onlineWallpapers"`
}

// UpdateConfig stores release channel information for the panel.
type UpdateConfig struct {
	LatestVersion string `json:"latestVersion"`
	Channel       string `json:"channel"`
	AllowUpload   bool   `json:"allowUpload"`
	// SourceURL 是版本清单与安装包所在的站点根地址，默认指向官方下载页。
	SourceURL string `json:"sourceUrl"`
	// Proxy 供面板访问外网更新源时使用；留空则跟随系统环境变量。
	Proxy string `json:"proxy"`
	// CheckEnabled 开启后由面板定时检查更新，发现新版本时推送通知。
	CheckEnabled bool `json:"checkEnabled"`
	// CheckIntervalHours 是自动检查的间隔小时数。
	CheckIntervalHours int `json:"checkIntervalHours"`
	// NotifyOnUpdate 为真时，发现新版本会通过已配置的通知渠道推送提醒。
	NotifyOnUpdate bool `json:"notifyOnUpdate"`
	// LastCheckAt 记录最近一次自动检查的时间。
	LastCheckAt string `json:"lastCheckAt"`
	// LastSeenVersion 记录最近一次通知过的新版本，避免重复提醒同一版本。
	LastSeenVersion string `json:"lastSeenVersion"`
}

// MonitorConfig controls host traffic sampling.
type MonitorConfig struct {
	Enable          bool    `json:"enable"`
	IntervalSeconds int     `json:"intervalSeconds"`
	Interface       string  `json:"interface"`
	RetentionDays   int     `json:"retentionDays"`
	MonthlyQuotaGB  float64 `json:"monthlyQuotaGb"`
}

// Config is the root YCFRP configuration document.
type Config struct {
	Port     int          `json:"port"`
	Username string       `json:"username"`
	Password string       `json:"password"`
	JWTSecret string      `json:"jwtSecret"`
	FRPS     FRPSConfig   `json:"frps"`
	FRPC     FRPCConfig   `json:"frpc"`
	Monitor  MonitorConfig `json:"monitor"`
	Notify   NotifyConfig `json:"notify"`
	UI       UIConfig     `json:"ui"`
	Updates  UpdateConfig `json:"update"`

	mu   sync.RWMutex `json:"-"`
	path string       `json:"-"`
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

// Default returns a configuration with sane defaults applied.
func Default() *Config {
	return &Config{
		Port:      DefaultPort,
		Username:  DefaultUser,
		Password:  DefaultPassword,
		JWTSecret: randomHex(24),
		FRPS: FRPSConfig{
			Enable:            true,
			BindAddr:          "0.0.0.0",
			BindPort:          7000,
			KCPBindPort:       0,
			QUICBindPort:      0,
			DashboardPort:     7500,
			DashboardUser:     DefaultUser,
			DashboardPwd:      DefaultPassword,
			VhostHTTPPort:     8080,
			VhostHTTPSPort:    8443,
			// token 留空 = 无认证，服务端与客户端一致即可直接连通。
			Token:             "",
			MaxPortsPerClient: 100,
			AllowPortsStart:   20000,
			AllowPortsEnd:     60000,
			LogLevel:          "info",
			LogMaxDays:        7,
			TransportTLS:      true,
		},
		FRPC: FRPCConfig{
			Enable:         false,
			ServerAddr:     "127.0.0.1",
			ServerPort:     7000,
			LoginFailExit:  false,
			LogLevel:       "info",
			LogMaxDays:     7,
			TransportTLS:   true,
			TransportProto: "tcp",
		},
		Monitor: MonitorConfig{
			Enable:          true,
			IntervalSeconds: 5,
			RetentionDays:   30,
			MonthlyQuotaGB:  0,
		},
		Notify: NotifyConfig{
			Enable:         false,
			MonitorErrors:  true,
			MonitorOffline: true,
			MonitorTraffic: false,
			QuietMinutes:   5,
			QQ: QQNotifierConfig{
				Protocol: "onebot",
				Host:     "127.0.0.1",
				Port:     5700,
			},
			Keywords: []string{"error", "failed", "timeout", "错误", "失败", "超时"},
		},
		UI: UIConfig{
			Theme:            "dark",
			AccentColor:      "#4f8cff",
			Wallpaper:        "w1",
			WallpaperOpacity: 1,
			SiteName:         version.ProductName,
			GlassEffect:      true,
		},
		Updates: UpdateConfig{
			LatestVersion:      version.Version,
			Channel:            "stable",
			AllowUpload:        true,
			SourceURL:          "https://ycfrp.yc1.cc.cd",
			CheckEnabled:       false,
			CheckIntervalHours: 6,
			NotifyOnUpdate:     true,
		},
	}
}

// Normalize fills in zero values so a partially written file still boots.
func (c *Config) Normalize() {
	def := Default()
	if c.Port <= 0 || c.Port > 65535 {
		c.Port = def.Port
	}
	if c.Username == "" {
		c.Username = def.Username
	}
	if c.Password == "" {
		c.Password = def.Password
	}
	if c.JWTSecret == "" {
		c.JWTSecret = def.JWTSecret
	}
	if c.FRPS.BindPort <= 0 {
		c.FRPS.BindPort = def.FRPS.BindPort
	}
	if c.FRPS.BindAddr == "" {
		c.FRPS.BindAddr = def.FRPS.BindAddr
	}
	if c.FRPS.DashboardPort <= 0 {
		c.FRPS.DashboardPort = def.FRPS.DashboardPort
	}
	if c.FRPS.DashboardUser == "" {
		c.FRPS.DashboardUser = def.FRPS.DashboardUser
	}
	if c.FRPS.DashboardPwd == "" {
		c.FRPS.DashboardPwd = def.FRPS.DashboardPwd
	}
	// 注意：token 留空要保持空（无认证），绝不能用默认值填充。
	// 服务端与客户端的 token 必须一致才能连上，若这里偷偷给服务端填一个
	// 随机值、而客户端保持空，两边就永远对不上，表现为「服务端不填 token
	// 客户端就连不上」。这是历史 bug 的根因。
	if c.FRPS.LogLevel == "" {
		c.FRPS.LogLevel = def.FRPS.LogLevel
	}
	if c.FRPS.LogMaxDays <= 0 {
		c.FRPS.LogMaxDays = def.FRPS.LogMaxDays
	}
	if c.FRPC.ServerAddr == "" {
		c.FRPC.ServerAddr = def.FRPC.ServerAddr
	}
	if c.FRPC.ServerPort <= 0 {
		c.FRPC.ServerPort = def.FRPC.ServerPort
	}
	if c.FRPC.LogLevel == "" {
		c.FRPC.LogLevel = def.FRPC.LogLevel
	}
	if c.FRPC.LogMaxDays <= 0 {
		c.FRPC.LogMaxDays = def.FRPC.LogMaxDays
	}
	if c.FRPC.TransportProto == "" {
		c.FRPC.TransportProto = def.FRPC.TransportProto
	}
	if c.Monitor.IntervalSeconds <= 0 {
		c.Monitor.IntervalSeconds = def.Monitor.IntervalSeconds
	}
	if c.Monitor.RetentionDays <= 0 {
		c.Monitor.RetentionDays = def.Monitor.RetentionDays
	}
	if c.UI.Theme == "" {
		c.UI.Theme = def.UI.Theme
	}
	if c.UI.AccentColor == "" {
		c.UI.AccentColor = def.UI.AccentColor
	}
	if c.UI.Wallpaper == "" {
		c.UI.Wallpaper = def.UI.Wallpaper
	}
	if c.UI.SiteName == "" {
		c.UI.SiteName = def.UI.SiteName
	}
	if c.UI.WallpaperOpacity < 0 || c.UI.WallpaperOpacity > 1 {
		c.UI.WallpaperOpacity = def.UI.WallpaperOpacity
	}
	if c.UI.NavLayout == "" {
		c.UI.NavLayout = "top"
	}
	if c.UI.UIStyle == "" {
		c.UI.UIStyle = "default"
	}
	if c.Notify.QQ.Protocol == "" {
		c.Notify.QQ.Protocol = "onebot"
	}
	if c.Notify.QQ.Host == "" {
		c.Notify.QQ.Host = "127.0.0.1"
	}
	if c.Notify.QQ.Port <= 0 {
		c.Notify.QQ.Port = 5700
	}
	if c.Notify.QuietMinutes <= 0 {
		c.Notify.QuietMinutes = 5
	}
	if c.Updates.LatestVersion == "" {
		c.Updates.LatestVersion = version.Version
	}
	if c.Updates.Channel == "" {
		c.Updates.Channel = "stable"
	}
	if strings.TrimSpace(c.Updates.SourceURL) == "" {
		c.Updates.SourceURL = def.Updates.SourceURL
	}
	if c.Updates.CheckIntervalHours <= 0 {
		c.Updates.CheckIntervalHours = def.Updates.CheckIntervalHours
	}
}

// Load reads the configuration file, creating a default one when missing.
func Load(path string) (*Config, error) {
	cfg := Default()
	cfg.path = path

	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		if err := cfg.Save(); err != nil {
			return nil, err
		}
		return cfg, nil
	}

	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	cfg.Normalize()
	return cfg, nil
}

// Save writes the configuration back to disk atomically.
func (c *Config) Save() error {
	c.mu.RLock()
	data, err := json.MarshalIndent(c, "", "  ")
	c.mu.RUnlock()
	if err != nil {
		return err
	}
	if c.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

// Path returns the on disk location of the configuration file.
func (c *Config) Path() string { return c.path }

// SetPath updates the on disk location, used by the GUI launcher.
func (c *Config) SetPath(path string) { c.path = path }

// Snapshot returns a deep copy of the current configuration.
func (c *Config) Snapshot() *Config {
	c.mu.RLock()
	defer c.mu.RUnlock()
	clone := &Config{
		Port:      c.Port,
		Username:  c.Username,
		Password:  c.Password,
		JWTSecret: c.JWTSecret,
		FRPS:      c.FRPS,
		FRPC:      c.FRPC,
		Monitor:   c.Monitor,
		Notify:    c.Notify,
		UI:        c.UI,
		Updates:   c.Updates,
	}
	clone.monitorCopy()
	return clone
}

func (c *Config) monitorCopy() {
	c.UI.OnlineWallpapers = append([]string(nil), c.UI.OnlineWallpapers...)
	c.Notify.Keywords = append([]string(nil), c.Notify.Keywords...)
	c.Notify.Channels = append([]NotifyChannel(nil), c.Notify.Channels...)
	c.Notify.QQ.GroupIDs = append([]string(nil), c.Notify.QQ.GroupIDs...)
}

// Update applies changes from fn under a write lock and persists the result.
func (c *Config) Update(fn func(*Config)) error {
	c.mu.Lock()
	fn(c)
	c.mu.Unlock()
	c.Normalize()
	return c.Save()
}

// Lock exposes the internal lock for coordinated reads.
func (c *Config) Lock()   { c.mu.Lock() }
func (c *Config) Unlock() { c.mu.Unlock() }
