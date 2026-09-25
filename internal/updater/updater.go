// Package updater 实现 YCFRP 的自更新能力：从下载页拉取版本清单、下载安装包、
// 解压出二进制，并用「助手进程」替换正在运行的可执行文件。
//
// 运行中的程序无法覆盖自身（Windows 会锁住镜像文件，Linux 会返回 ETXTBSY），
// 因此替换动作交给一个派生出去的助手进程完成：助手由当前二进制复制而来，
// 等父进程退出后再落盘并重新拉起面板。
package updater

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Manifest 是下载页提供的版本清单，字段与 version.json 一一对应。
type Manifest struct {
	Version    string            `json:"version"`
	Kernel     string            `json:"kernel"`
	ReleasedAt string            `json:"releasedAt"`
	Notes      string            `json:"notes"`
	Platforms  map[string]string `json:"platforms"`
	// Changelog 是历次版本的更新说明，供面板「更新」页展示。
	Changelog []ChangelogEntry `json:"changelog"`
}

// ChangelogEntry 是一个版本的更新说明。
type ChangelogEntry struct {
	Version string   `json:"version"`
	Date    string   `json:"date"`
	// Kind 取值：add 新增 / fix 修复 / change 调整。
	Kind  string   `json:"kind"`
	Items []string `json:"items"`
}

// CurrentPlatform 返回当前运行环境在清单里的键名，例如 linux-amd64。
func CurrentPlatform() string {
	arch := runtime.GOARCH
	switch arch {
	case "arm":
		// 安装包按 ARMv7 打包，清单里统一记为 armv7。
		arch = "armv7"
	case "386":
		arch = "amd64"
	}
	return runtime.GOOS + "-" + arch
}

// ManifestURL 由站点根地址拼出版本清单地址。
func ManifestURL(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return ""
	}
	return base + "/version.json"
}

// SameOrigin 判断下载地址是否与更新源同源，避免面板被诱导去下载任意地址。
func SameOrigin(base, target string) bool {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	target = strings.TrimSpace(target)
	if base == "" || target == "" {
		return false
	}
	return strings.HasPrefix(target, base+"/")
}

// CompareVersions 比较两个 sYYMM.NNN 形式的版本号，返回 a 相对 b 的方向：
// 大于 0 表示 a 更新，0 表示相同，小于 0 表示 a 更旧。无法解析时按字符串比较，
// 保证任何意外格式都不会被误判成「有更新」。
func CompareVersions(a, b string) int {
	an, aok := parseVersion(a)
	bn, bok := parseVersion(b)
	if !aok || !bok {
		return strings.Compare(strings.TrimSpace(a), strings.TrimSpace(b))
	}
	switch {
	case an > bn:
		return 1
	case an < bn:
		return -1
	default:
		return 0
	}
}

// parseVersion 把 s2609.003 解析成一个可比较的整数：年月与修订号各占一段，
// 修订号补足四位，避免 003 与 30 这类位数差异造成误判。
func parseVersion(v string) (int64, bool) {
	v = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(v), "s"))
	if v == "" {
		return 0, false
	}
	dot := strings.IndexByte(v, '.')
	if dot < 0 {
		return 0, false
	}
	ym, rev := v[:dot], v[dot+1:]
	if ym == "" || rev == "" {
		return 0, false
	}
	var ymNum, revNum int64
	for _, c := range ym {
		if c < '0' || c > '9' {
			return 0, false
		}
		ymNum = ymNum*10 + int64(c-'0')
	}
	for _, c := range rev {
		if c < '0' || c > '9' {
			return 0, false
		}
		revNum = revNum*10 + int64(c-'0')
	}
	return ymNum*10000 + revNum, true
}

// FetchManifest 拉取并解析版本清单。proxy 为空时走直连。
func FetchManifest(base, proxy string, timeout time.Duration) (*Manifest, error) {
	u := ManifestURL(base)
	if u == "" {
		return nil, fmt.Errorf("未配置更新源地址")
	}
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	client := newHTTPClient(proxy, timeout)
	resp, err := client.Get(u)
	if err != nil {
		return nil, fmt.Errorf("无法连接更新源：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("更新源返回状态 %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("读取版本清单失败：%w", err)
	}
	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("版本清单格式不正确：%w", err)
	}
	if strings.TrimSpace(m.Version) == "" {
		return nil, fmt.Errorf("版本清单缺少版本号")
	}
	return &m, nil
}

// Download 把安装包下载到 destDir，返回落盘路径。文件名取自 URL 末段，
// 缺失时按平台生成一个安全的名字。
func Download(rawURL, destDir, proxy string, timeout time.Duration, progress func(int64, int64)) (string, error) {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	client := newHTTPClient(proxy, timeout)
	resp, err := client.Get(rawURL)
	if err != nil {
		return "", fmt.Errorf("下载安装包失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载安装包失败：更新源返回状态 %d", resp.StatusCode)
	}

	name := packageName(rawURL)
	target := filepath.Join(destDir, name)
	tmp := target + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return "", fmt.Errorf("创建下载文件失败：%w", err)
	}
	defer func() {
		_ = f.Close()
		_ = os.Remove(tmp)
	}()

	total := resp.ContentLength
	var written int64
	buf := make([]byte, 256<<10)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return "", fmt.Errorf("写入下载文件失败：%w", werr)
			}
			written += int64(n)
			if progress != nil {
				progress(written, total)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", fmt.Errorf("下载中断：%w", readErr)
		}
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("关闭下载文件失败：%w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		return "", fmt.Errorf("保存下载文件失败：%w", err)
	}
	return target, nil
}

// packageName 从下载地址里取出文件名，剔掉查询串并防止路径穿越。
func packageName(rawURL string) string {
	u, err := url.Parse(rawURL)
	path := rawURL
	if err == nil && u.Path != "" {
		path = u.Path
	}
	name := filepath.Base(path)
	if name == "" || name == "." || name == "/" || strings.Contains(name, "..") {
		return "ycfrp-" + CurrentPlatform() + ".bin"
	}
	return name
}

// newHTTPClient 构造带超时与可选代理的客户端。更新源在外网，NAS 等环境
// 往往只能经代理访问，因此这里允许单独指定代理。
func newHTTPClient(proxy string, timeout time.Duration) *http.Client {
	transport := &http.Transport{}
	if p := strings.TrimSpace(proxy); p != "" {
		if pu, err := url.Parse(p); err == nil && pu.Host != "" {
			transport.Proxy = http.ProxyURL(pu)
		}
	} else {
		transport.Proxy = http.ProxyFromEnvironment
	}
	return &http.Client{Transport: transport, Timeout: timeout}
}
