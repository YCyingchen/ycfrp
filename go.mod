module github.com/ycfrp/ycfrp

go 1.25.0

require (
	github.com/fatedier/frp v0.71.0
	github.com/fatedier/golib v0.8.2
	github.com/gorilla/websocket v1.5.3
	github.com/jchv/go-webview2 v0.0.0-20260205173254-56598839c808
	github.com/shirou/gopsutil/v4 v4.26.8
	golang.org/x/crypto v0.54.0
	golang.org/x/text v0.40.0
)

require (
	github.com/Azure/go-ntlmssp v0.1.0 // indirect
	github.com/armon/go-socks5 v0.0.0-20160902184237-e75332964ef5 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.2.0 // indirect
	github.com/coreos/go-oidc/v3 v3.18.0 // indirect
	github.com/ebitengine/purego v0.10.2 // indirect
	github.com/go-jose/go-jose/v4 v4.1.4 // indirect
	github.com/go-ole/go-ole v1.2.6 // indirect
	github.com/golang/snappy v0.0.4 // indirect
	github.com/gorilla/mux v1.8.1 // indirect
	github.com/hashicorp/yamux v0.1.1 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/jchv/go-winloader v0.0.0-20250406163304-c1995be93bd1 // indirect
	github.com/klauspost/cpuid/v2 v2.3.0 // indirect
	github.com/klauspost/reedsolomon v1.12.0 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/lufia/plan9stats v0.0.0-20211012122336-39d0f177ccd0 // indirect
	github.com/pelletier/go-toml/v2 v2.2.0 // indirect
	github.com/pires/go-proxyproto v0.15.0 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/power-devops/perfstat v0.0.0-20240221224432-82ca36839d55 // indirect
	github.com/prometheus/client_golang v1.19.1 // indirect
	github.com/prometheus/client_model v0.5.0 // indirect
	github.com/prometheus/common v0.48.0 // indirect
	github.com/prometheus/procfs v0.12.0 // indirect
	github.com/quic-go/quic-go v0.60.0 // indirect
	github.com/samber/lo v1.47.0 // indirect
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e // indirect
	github.com/songgao/water v0.0.0-20200317203138-2b4b6d7c09d8 // indirect
	github.com/spf13/cobra v1.8.0 // indirect
	github.com/spf13/pflag v1.0.5 // indirect
	github.com/templexxx/cpu v0.1.1 // indirect
	github.com/templexxx/xorsimd v0.4.3 // indirect
	github.com/tjfoc/gmsm v1.4.1 // indirect
	github.com/tklauser/go-sysconf v0.3.16 // indirect
	github.com/tklauser/numcpus v0.11.0 // indirect
	github.com/vishvananda/netlink v1.3.0 // indirect
	github.com/vishvananda/netns v0.0.4 // indirect
	github.com/xtaci/kcp-go/v5 v5.6.13 // indirect
	github.com/yusufpapurcu/wmi v1.2.4 // indirect
	golang.org/x/net v0.56.0 // indirect
	golang.org/x/oauth2 v0.36.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/time v0.10.0 // indirect
	golang.zx2c4.com/wintun v0.0.0-20230126152724-0fa3db229ce2 // indirect
	golang.zx2c4.com/wireguard v0.0.0-20231211153847-12269c276173 // indirect
	google.golang.org/protobuf v1.36.5 // indirect
	gopkg.in/ini.v1 v1.67.0 // indirect
	gopkg.in/yaml.v2 v2.4.0 // indirect
	k8s.io/apimachinery v0.28.8 // indirect
	k8s.io/utils v0.0.0-20230406110748-d93618cff8a2 // indirect
	sigs.k8s.io/json v0.0.0-20221116044647-bc3834ca7abd // indirect
	sigs.k8s.io/yaml v1.3.0 // indirect
)

replace github.com/hashicorp/yamux => github.com/fatedier/yamux v0.0.0-20250825093530-d0154be01cd6

// YCFRP 内嵌 frp 内核。这里使用本地副本，原因：官方 v0.71.0 的 server.Service.Close()
// 不会关闭 HTTP vhost 监听端口（VhostHTTPPort，默认 8080），导致面板在应用新配置后
// 原地重启内核时端口被旧实例占用而失败。本地副本 third_party/frp 已修复该问题。
replace github.com/fatedier/frp => ./third_party/frp
