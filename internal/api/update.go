package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	daemonpkg "github.com/ycfrp/ycfrp/internal/daemon"
	"github.com/ycfrp/ycfrp/internal/logx"
	"github.com/ycfrp/ycfrp/internal/updater"
	"github.com/ycfrp/ycfrp/internal/version"
)

// 默认更新源：下载页所在的站点根地址，面板据此读取 /version.json。
const defaultUpdateBase = "https://ycfrp.yc1.cc.cd"

// maxPackageBytes 限制上传安装包的体积，避免一次请求占满磁盘。
const maxPackageBytes = 200 << 20

// updateSource 汇总一次更新检查所需的源信息：站点地址与可选代理。
// 面板多数部署在内网，读取外网下载页往往需要经过代理，因此允许单独指定。
func (s *Server) updateSource() (base, proxy string) {
	cfg := s.app.Cfg.Snapshot()
	base = strings.TrimSpace(cfg.Updates.SourceURL)
	if base == "" {
		base = defaultUpdateBase
	}
	return base, strings.TrimSpace(cfg.Updates.Proxy)
}

// handleUpdateCheck 拉取远端版本清单并与当前版本比较。
func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	base, proxy := s.updateSource()
	m, err := updater.FetchManifest(base, proxy, 20*time.Second)
	if err != nil {
		s.app.Logf(logx.LevelWarn, "更新", "检查更新失败：%v", err)
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}

	platform := updater.CurrentPlatform()
	pkgURL := m.Platforms[platform]
	cmp := updater.CompareVersions(m.Version, version.Version)
	s.app.Logf(logx.LevelInfo, "更新", "检查更新：当前 %s，最新 %s", version.Version, m.Version)

	inDocker := s.app.Mode == "docker"
	// 容器部署下的自助更新依赖 docker.sock：拉取镜像后重启容器。
	dockerSelf := inDocker && dockerSockAvailable()
	writeOK(w, map[string]any{
		"current":    version.Version,
		"latest":     m.Version,
		"kernel":     m.Kernel,
		"releasedAt": m.ReleasedAt,
		"notes":      m.Notes,
		"hasUpdate":  cmp > 0,
		"platform":   platform,
		"packageUrl": pkgURL,
		"available":  pkgURL != "",
		"source":     base,
		"changelog":  m.Changelog,
		"docker":     inDocker,
		// 容器部署下只要挂载了 docker.sock 就能自助拉取镜像更新。
		"canAuto":     cmp > 0 && ((!inDocker && pkgURL != "") || dockerSelf),
		"dockerSelf":  dockerSelf,
		"command":     dockerUpgradeCommand,
		"graceful":    inDocker,
	})
}

// handleUpdateApply 从更新源下载安装包并执行替换。
func (s *Server) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 POST")
		return
	}
	if s.app.Mode == "docker" {
		writeErr(w, http.StatusBadRequest,
			"当前为 Docker 部署，请在宿主机执行："+dockerUpgradeCommand)
		return
	}
	var body struct {
		URL string `json:"url"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	base, proxy := s.updateSource()
	target := strings.TrimSpace(body.URL)
	if target == "" {
		m, err := updater.FetchManifest(base, proxy, 20*time.Second)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		target = m.Platforms[updater.CurrentPlatform()]
		if target == "" {
			writeErr(w, http.StatusBadRequest,
				fmt.Sprintf("更新源未提供当前平台（%s）的安装包", updater.CurrentPlatform()))
			return
		}
	} else if !updater.SameOrigin(base, target) {
		writeErr(w, http.StatusBadRequest, "下载地址必须来自配置的更新源")
		return
	}

	stageDir := filepath.Join(s.app.DataDir, "update")
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		writeErr(w, http.StatusInternalServerError, "创建更新目录失败："+err.Error())
		return
	}
	s.app.Logf(logx.LevelInfo, "更新", "开始下载安装包：%s", target)

	archive, err := updater.Download(target, stageDir, proxy, 10*time.Minute, nil)
	if err != nil {
		s.app.Logf(logx.LevelError, "更新", "下载安装包失败：%v", err)
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	s.app.Logf(logx.LevelInfo, "更新", "安装包已下载：%s", filepath.Base(archive))

	staged, err := s.stageFromPackage(archive)
	if err != nil {
		s.app.Logf(logx.LevelError, "更新", "解压安装包失败：%v", err)
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.applyStaged(w, staged, "在线更新")
}

// handleUpdateUpload 接收用户上传的安装包并执行替换。
func (s *Server) handleUpdateUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 POST")
		return
	}
	if s.app.Mode == "docker" {
		writeErr(w, http.StatusBadRequest,
			"当前为 Docker 部署，不支持上传替换；请在宿主机执行："+dockerUpgradeCommand)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxPackageBytes)
	if err := r.ParseMultipartForm(maxPackageBytes); err != nil {
		writeErr(w, http.StatusBadRequest, "上传内容过大或格式不正确")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "未收到上传文件")
		return
	}
	defer file.Close()

	stageDir := filepath.Join(s.app.DataDir, "update")
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		writeErr(w, http.StatusInternalServerError, "创建更新目录失败："+err.Error())
		return
	}
	name := safePackageName(header.Filename)
	archive := filepath.Join(stageDir, name)
	dst, err := os.Create(archive)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "保存上传文件失败："+err.Error())
		return
	}
	defer dst.Close()
	written, err := copyLimited(dst, file, maxPackageBytes)
	if err != nil {
		_ = os.Remove(archive)
		writeErr(w, http.StatusInternalServerError, "写入上传文件失败："+err.Error())
		return
	}
	s.app.Logf(logx.LevelInfo, "更新", "已上传安装包 %s（%d 字节）", name, written)

	staged, err := s.stageFromPackage(archive)
	if err != nil {
		s.app.Logf(logx.LevelError, "更新", "解压上传包失败：%v", err)
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.applyStaged(w, staged, "上传更新")
}

// stageFromPackage 把安装包解出可执行文件并校验架构，返回待替换的临时文件。
func (s *Server) stageFromPackage(archive string) (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("无法定位当前程序：%w", err)
	}
	// 优先挑选与当前运行程序同名的可执行文件，Windows 包里含 GUI 与控制台两版。
	staged := filepath.Join(filepath.Dir(archive), "ycfrp.new")
	if _, err := updater.ExtractBinary(archive, staged, filepath.Base(self)); err != nil {
		return "", err
	}
	if err := updater.VerifyBinary(staged); err != nil {
		return "", err
	}
	return staged, nil
}

// applyStaged 拉起助手进程完成替换。响应先于替换返回，让前端能收到提示；
// 随后面板会退出，助手在后台把新程序就位并重新拉起服务。
func (s *Server) applyStaged(w http.ResponseWriter, staged, source string) {
	self, err := os.Executable()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "无法定位当前程序："+err.Error())
		return
	}
	cfg := s.app.Cfg.Snapshot()
	req := updater.ApplyRequest{
		Target:    self,
		Staged:    staged,
		ParentPID: os.Getpid(),
		DataDir:   s.app.DataDir,
		Port:      cfg.Port,
	}
	if err := updater.LaunchHelper(req); err != nil {
		s.app.Logf(logx.LevelError, "更新", "启动更新助手失败：%v", err)
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.app.Logf(logx.LevelWarn, "更新", "%s：新程序已就绪，面板即将重启完成替换", source)

	writeOK(w, map[string]any{
		"restarting": true,
		"target":     self,
		"staged":     staged,
		"message":    "更新已开始，面板将在数秒内自动重启",
	})

	// 给响应留出写出时间，随后主动退出，把二进制交给助手替换。
	go func() {
		time.Sleep(1200 * time.Millisecond)
		s.app.Logf(logx.LevelWarn, "更新", "正在退出以便完成替换 ...")
		time.Sleep(300 * time.Millisecond)
		daemonpkg.RequestStop(s.app.DataDir)
	}()
}

// safePackageName 清洗上传文件名，只保留基本名并限制扩展名。
func safePackageName(name string) string {
	base := filepath.Base(strings.TrimSpace(name))
	if base == "" || base == "." || strings.Contains(base, "..") {
		base = "ycfrp-" + updater.CurrentPlatform() + ".pkg"
	}
	lower := strings.ToLower(base)
	for _, ext := range []string{".zip", ".tar.gz", ".tgz"} {
		if strings.HasSuffix(lower, ext) {
			return "upload-" + base
		}
	}
	// 允许直接上传裸二进制（无扩展名或 .exe）。
	return "upload-" + base
}

// copyLimited 复制上传流并在超出上限时立即报错，避免写满磁盘。
func copyLimited(dst *os.File, src io.Reader, limit int64) (int64, error) {
	buf := make([]byte, 256<<10)
	var written int64
	for {
		n, readErr := src.Read(buf)
		if n > 0 {
			written += int64(n)
			if written > limit {
				return written, fmt.Errorf("文件超过 %d MB 上限", limit>>20)
			}
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return written, werr
			}
		}
		if readErr == io.EOF {
			return written, nil
		}
		if readErr != nil {
			return written, readErr
		}
	}
}

// handleUpdateCheckNow 立即执行一次完整检查（与自动检查同一条逻辑），
// 因此发现新版本时也会按设置推送通知。
func (s *Server) handleUpdateCheckNow(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 POST")
		return
	}
	s.app.CheckUpdateNow()
	res := s.app.LastUpdateCheck()
	if res == nil {
		writeErr(w, http.StatusBadGateway, "检查未返回结果")
		return
	}
	if res.Err != "" {
		writeErr(w, http.StatusBadGateway, res.Err)
		return
	}
	cfg := s.app.Cfg.Snapshot()
	inDocker := s.app.Mode == "docker"
	dockerSelf := inDocker && dockerSockAvailable()
	cmp := updater.CompareVersions(res.Latest, version.Version)
	writeOK(w, map[string]any{
		"current":    version.Version,
		"latest":     res.Latest,
		"hasUpdate":  res.HasUpdate,
		"notes":      res.Notes,
		"releasedAt": res.ReleasedAt,
		"platform":   res.Platform,
		"packageUrl": res.PackageURL,
		"available":  res.PackageURL != "",
		"source":     strings.TrimSpace(cfg.Updates.SourceURL),
		"docker":     inDocker,
		"dockerSelf": dockerSelf,
		"canAuto":    cmp > 0 && ((!inDocker && res.PackageURL != "") || dockerSelf),
		"command":    dockerUpgradeCommand,
		"graceful":   inDocker,
		"notified":   cfg.Updates.NotifyOnUpdate && cfg.Notify.Enable,
	})
}

// handleQQBind 处理扫码绑定的三个动作：
//   - POST /api/qqbot/bind          生成任务与二维码
//   - GET  /api/qqbot/bind          查询绑定进度
//   - DELETE /api/qqbot/bind        取消绑定
//
// 走官方 connect.html 扫码页，扫码后平台直接把 AppID/AppSecret 回给面板，
// 因此无需用户手抄凭据，也不依赖任何第三方工具。
func (s *Server) handleQQBind(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		var body struct {
			Source string `json:"source"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		source := strings.TrimSpace(body.Source)
		if source == "" {
			source = version.ProductName
		}
		info, err := s.app.StartQQBind(source)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		writeOK(w, info)
	case http.MethodGet:
		// 有进行中的会话就直接回报，没有时返回空对象让前端展示入口。
		if info := s.app.BindSessionInfo(); info != nil {
			writeOK(w, info)
			return
		}
		writeOK(w, nil)
	case http.MethodDelete:
		s.app.CancelQQBind()
		writeOK(w, nil)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "不支持的方法")
	}
}

// handleQQBindPoll 轮询绑定结果；成功后凭据会自动写入配置。
func (s *Server) handleQQBindPoll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "不支持的方法")
		return
	}
	info, err := s.app.PollQQBind()
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeOK(w, info)
}

// handleQQBotStatus 返回 QQ 机器人网关的连接状态与已发现的 openid。
func (s *Server) handleQQBotStatus(w http.ResponseWriter, r *http.Request) {
	cfg := s.app.Cfg.Snapshot().Notify.QQOfficial
	writeOK(w, map[string]any{
		"status":     s.app.QQBotStatus(),
		"listen":     cfg.Listen,
		"enable":     cfg.Enable,
		"hasCreds":   strings.TrimSpace(cfg.AppID) != "" && strings.TrimSpace(cfg.AppSecret) != "",
		"discovered": cfg.Discovered,
	})
}

// handleQQBotDiscover 主动查询已发现的 openid（与状态接口分离，便于前端轮询）。
func (s *Server) handleQQBotDiscover(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg := s.app.Cfg.Snapshot().Notify.QQOfficial
		writeOK(w, map[string]any{"discovered": cfg.Discovered})
	case http.MethodDelete, http.MethodPost:
		s.app.ClearDiscoveredOpenID()
		s.app.Logf(logx.LevelInfo, "QQ机器人", "已清空自动发现的 openid 记录")
		writeOK(w, nil)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "不支持的方法")
	}
}

// updateInfo 供 /api/meta 向面板暴露更新源配置，让「界面」页能回显。
func (s *Server) updateInfo() map[string]any {
	cfg := s.app.Cfg.Snapshot()
	base := strings.TrimSpace(cfg.Updates.SourceURL)
	if base == "" {
		base = defaultUpdateBase
	}
	info := map[string]any{
		"source":        base,
		"proxy":         cfg.Updates.Proxy,
		"allowUpload":   cfg.Updates.AllowUpload,
		"checkEnabled":  cfg.Updates.CheckEnabled,
		"checkHours":    cfg.Updates.CheckIntervalHours,
		"notifyOnUpdate": cfg.Updates.NotifyOnUpdate,
		"lastCheckAt":   cfg.Updates.LastCheckAt,
		"lastSeen":      cfg.Updates.LastSeenVersion,
		"current":       version.Version,
		"platform":      updater.CurrentPlatform(),
		"goos":          runtime.GOOS,
		"docker":        s.app.Mode == "docker",
		"mode":          s.app.Mode,
	}
	// 附带最近一次自动检查的结论，面板打开即能看到结果。
	if last := s.app.LastUpdateCheck(); last != nil {
		info["lastResult"] = map[string]any{
			"checkedAt":  last.CheckedAt.Format("2006-01-02 15:04:05"),
			"latest":     last.Latest,
			"hasUpdate":  last.HasUpdate,
			"err":        last.Err,
			"notes":      last.Notes,
			"releasedAt": last.ReleasedAt,
		}
	}
	return info
}

// dockerUpgradeCommand 是容器部署下的升级方式。
//
// 容器内不能替换自身二进制：镜像层是只读的，且没有权限操作宿主机的 docker。
// 因此 Docker 部署统一使用 latest 标签，由用户在宿主机执行 compose 拉取重建，
// 面板只负责发现新版本并把命令呈现出来。
const dockerUpgradeCommand = "docker compose pull && docker compose up -d"

// updateModeInfo 描述当前部署方式应采用的更新手段，供前端切换界面形态。
func (s *Server) updateModeInfo() map[string]any {
	inDocker := s.app.Mode == "docker"
	return map[string]any{
		"docker":     inDocker,
		"mode":       s.app.Mode,
		"selfUpdate": !inDocker,
		"command":    dockerUpgradeCommand,
		"image":      dockerImage,
		"imageTag":   dockerArchTag(),
		// 容器部署下是否支持自助更新，取决于是否挂载了 docker.sock。
		"dockerSelfUpdate": inDocker && dockerSockAvailable(),
	}
}

// dockerSockAvailable 判断 docker.sock 是否已挂载进容器，决定容器内能否自助更新。
func dockerSockAvailable() bool {
	_, err := os.Stat(dockerSock)
	return err == nil
}
