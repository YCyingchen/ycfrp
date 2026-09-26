package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	daemonpkg "github.com/ycfrp/ycfrp/internal/daemon"
	"github.com/ycfrp/ycfrp/internal/logx"
)

// 容器部署下的自助更新：面板通过宿主机挂载进来的 /var/run/docker.sock 直连
// Docker Engine API，实现在线拉取镜像或载入本地镜像包，然后重启自身容器。
// 镜像层只读、面板不能替换自身二进制，所以必须借助宿主机 docker 完成。

const (
	dockerSock          = "/var/run/docker.sock"
	dockerImage         = "ycyingchen/ycfrp"
	maxDockerPkgBytes   = 4 << 30 // 镜像 tar 上限 4 GB。
	dockerApplyTimeout  = 20 * time.Minute
)

// dockerClient 复用同一个 unix socket 连接，避免每次请求都重新拨号。
type dockerClient struct {
	http *http.Client
}

func newDockerClient() (*dockerClient, error) {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", dockerSock)
		},
	}
	return &dockerClient{http: &http.Client{Transport: tr, Timeout: dockerApplyTimeout}}, nil
}

// do 执行一次 Docker Engine API 请求，返回响应体与状态码。
func (c *dockerClient) do(method, path string, body io.Reader, contentType string) ([]byte, int, error) {
	req, err := http.NewRequest(method, "http://docker"+path, body)
	if err != nil {
		return nil, 0, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return data, resp.StatusCode, nil
}

// handleDockerApply 处理容器部署的「在线更新」：拉取指定标签的最新镜像并重启容器。
func (s *Server) handleDockerApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 POST")
		return
	}
	if s.app.Mode != "docker" {
		writeErr(w, http.StatusBadRequest, "当前不是 Docker 部署，请使用普通的在线/上传更新")
		return
	}
	var body struct {
		Tag string `json:"tag"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	tag := strings.TrimSpace(body.Tag)
	if tag == "" {
		// 按当前容器架构选择对应标签，避免拉错平台的镜像。
		tag = dockerArchTag()
	}
	image := dockerImage + ":" + tag

	container, err := s.dockerContainerName()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	cli, err := newDockerClient()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "连接 Docker 失败："+err.Error())
		return
	}

	s.app.Logf(logx.LevelInfo, "更新", "Docker 在线更新：拉取镜像 %s", image)
	if err := pullImage(cli, dockerImage, tag); err != nil {
		s.app.Logf(logx.LevelError, "更新", "拉取镜像失败：%v", err)
		writeErr(w, http.StatusBadGateway, "拉取镜像失败："+err.Error())
		return
	}

	writeOK(w, map[string]any{
		"restarting": true,
		"message":    "镜像已拉取，正在用新镜像重建容器，稍后自动恢复",
	})
	go s.recreateDockerContainer(container, image, "在线更新")
}

// handleDockerUpload 处理容器部署的「本地更新」：接收一个 docker save 出的
// 镜像 tar 包，载入后重启容器。
func (s *Server) handleDockerUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 POST")
		return
	}
	if s.app.Mode != "docker" {
		writeErr(w, http.StatusBadRequest, "当前不是 Docker 部署，请使用普通的在线/上传更新")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxDockerPkgBytes)
	if err := r.ParseMultipartForm(maxDockerPkgBytes); err != nil {
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
	archive := filepath.Join(stageDir, "docker-image.tar")
	dst, err := os.Create(archive)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "保存上传文件失败："+err.Error())
		return
	}
	written, err := io.Copy(dst, file)
	dst.Close()
	if err != nil {
		_ = os.Remove(archive)
		writeErr(w, http.StatusInternalServerError, "写入上传文件失败："+err.Error())
		return
	}
	s.app.Logf(logx.LevelInfo, "更新", "已上传 Docker 镜像包 %s（%d 字节）", header.Filename, written)

	container, err := s.dockerContainerName()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	cli, err := newDockerClient()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "连接 Docker 失败："+err.Error())
		return
	}

	s.app.Logf(logx.LevelInfo, "更新", "载入本地镜像包 ...")
	loadedRef, err := loadImage(cli, archive)
	if err != nil {
		s.app.Logf(logx.LevelError, "更新", "载入镜像失败：%v", err)
		writeErr(w, http.StatusBadRequest, "载入镜像失败："+err.Error())
		return
	}
	image := loadedRef
	if image == "" {
		image = dockerImage + ":" + dockerArchTag()
	}

	writeOK(w, map[string]any{
		"restarting": true,
		"message":    "镜像已载入，正在用新镜像重建容器，稍后自动恢复",
	})
	go s.recreateDockerContainer(container, image, "本地更新")
}

// dockerContainerName 反查自身容器名/ID，供 restart 使用。
//
// 优先级：YCFRP_CONTAINER_NAME 环境变量 > /proc/self/cgroup 里的容器 ID >
// HOSTNAME 环境变量（docker 默认把容器 ID 前 12 位设为 hostname）。
func (s *Server) dockerContainerName() (string, error) {
	if name := strings.TrimSpace(os.Getenv("YCFRP_CONTAINER_NAME")); name != "" {
		return name, nil
	}
	if id, err := dockerSelfID(); err == nil {
		return id, nil
	}
	if host := strings.TrimSpace(os.Getenv("HOSTNAME")); host != "" {
		return host, nil
	}
	return "", fmt.Errorf("无法识别当前容器（可设置 YCFRP_CONTAINER_NAME 环境变量指定）")
}

// dockerSelfID 从 /proc/self/cgroup 提取容器 ID（docker 默认写入完整 ID）。
func dockerSelfID() (string, error) {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if idx := strings.LastIndex(line, "/"); idx >= 0 {
			id := line[idx+1:]
			if len(id) >= 12 {
				return id, nil
			}
		}
	}
	return "", fmt.Errorf("cgroup 未提供容器 ID")
}

// pullImage 通过 Docker API 拉取镜像，返回遇到的错误（响应流里的 error 也会被识别）。
func pullImage(cli *dockerClient, image, tag string) error {
	query := fmt.Sprintf("/images/create?fromImage=%s&tag=%s", image, tag)
	data, status, err := cli.do(http.MethodPost, query, nil, "")
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("%s", statusText(status, data))
	}
	// 拉取过程是流式 JSON，逐行解析，有 error 字段则视为失败。
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev struct {
			Error string `json:"error"`
		}
		if json.Unmarshal([]byte(line), &ev) == nil && ev.Error != "" {
			return fmt.Errorf("%s", ev.Error)
		}
	}
	return nil
}

// loadImage 通过 Docker API 载入镜像 tar 包。
// loadImage 载入镜像 tar 包，返回解析出的镜像引用（如 ycyingchen/ycfrp:tag）。
func loadImage(cli *dockerClient, tarPath string) (string, error) {
	f, err := os.Open(tarPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, status, err := cli.do(http.MethodPost, "/images/load", f, "application/x-tar")
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("%s", statusText(status, data))
	}
	return loadedImageRef(data), nil
}

// loadedImageRef 从 docker load 的流式响应里取出 "Loaded image: <ref>"。
func loadedImageRef(data []byte) string {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev struct {
			Stream string `json:"stream"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		if i := strings.Index(ev.Stream, "Loaded image: "); i >= 0 {
			return strings.TrimSpace(strings.TrimPrefix(ev.Stream, "Loaded image: "))
		}
	}
	return ""
}

// recreateDockerContainer 用新镜像按原配置重建容器并切换运行。
//
// restart 无法切换镜像（容器创建时即绑定镜像），因此这里走重建：
// 先改名旧容器让出名字 → 用原名+新镜像建新容器 → 关停当前进程释放端口
// 并落盘 → 启动新容器 → 强制删除旧容器（当前进程随之退出）。
func (s *Server) recreateDockerContainer(container, image, source string) {
	time.Sleep(1500 * time.Millisecond) // 等 HTTP 响应先发给客户端
	cli, err := newDockerClient()
	if err != nil {
		s.app.Logf(logx.LevelError, "更新", "%s：连接 Docker 失败：%v", source, err)
		return
	}
	name, config, hostConfig, err := cli.inspectContainer(container)
	if err != nil {
		s.app.Logf(logx.LevelError, "更新", "%s：读取容器配置失败：%v", source, err)
		return
	}
	if name == "" {
		name = container
	}

	// 1) 改名旧容器，让出名字（运行中的容器改名不影响其进程）。
	tmpName := fmt.Sprintf("%s-old-%d", name, time.Now().UnixNano())
	if _, status, err := cli.do(http.MethodPost, "/containers/"+container+"/rename?name="+tmpName, nil, ""); err != nil || (status != http.StatusNoContent && status != http.StatusOK) {
		s.app.Logf(logx.LevelError, "更新", "%s：重命名旧容器失败：%v", source, err)
		return
	}

	// 2) 用原名 + 新镜像 + 原配置创建新容器（先不启动）。
	config["Image"] = image
	createBody := make(map[string]any, len(config)+1)
	for k, v := range config {
		createBody[k] = v
	}
	createBody["HostConfig"] = hostConfig
	payload, _ := json.Marshal(createBody)
	data, status, err := cli.do(http.MethodPost, "/containers/create?name="+name, bytes.NewReader(payload), "application/json")
	if err != nil || (status != http.StatusCreated && status != http.StatusOK) {
		s.app.Logf(logx.LevelError, "更新", "%s：创建新容器失败（%d）：%v", source, status, err)
		// 恢复旧容器名字，尽量回到可用状态。
		_, _, _ = cli.do(http.MethodPost, "/containers/"+container+"/rename?name="+name, nil, "")
		return
	}
	var created struct {
		Id string `json:"Id"`
	}
	_ = json.Unmarshal(data, &created)
	if created.Id == "" {
		s.app.Logf(logx.LevelError, "更新", "%s：创建新容器响应异常", source)
		return
	}

	// 3) 关停当前进程（释放 38080 与 frp 端口）并落盘。
	s.shutdownForRecreate()

	// 4) 启动新容器。
	if _, status, err := cli.do(http.MethodPost, "/containers/"+created.Id+"/start", nil, ""); err != nil || (status != http.StatusNoContent && status != http.StatusOK) {
		s.app.Logf(logx.LevelError, "更新", "%s：启动新容器失败（%d）：%v", source, status, err)
		return
	}
	s.app.Logf(logx.LevelInfo, "更新", "%s：已启动新容器 %s（%s）", source, name, image)

	// 5) 强制删除旧容器；当前进程随容器退出，切换完成。
	_, _, _ = cli.do(http.MethodDelete, "/containers/"+container+"?force=true", nil, "")
}

// inspectContainer 返回容器的名字（去斜杠）、Config 与 HostConfig。
func (c *dockerClient) inspectContainer(id string) (name string, config, hostConfig map[string]any, err error) {
	data, status, err := c.do(http.MethodGet, "/containers/"+id+"/json", nil, "")
	if err != nil {
		return "", nil, nil, err
	}
	if status != http.StatusOK {
		return "", nil, nil, fmt.Errorf("%s", statusText(status, data))
	}
	var raw struct {
		Name       string         `json:"Name"`
		Config     map[string]any `json:"Config"`
		HostConfig map[string]any `json:"HostConfig"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return "", nil, nil, err
	}
	return strings.TrimPrefix(raw.Name, "/"), raw.Config, raw.HostConfig, nil
}

// shutdownForRecreate 关停面板自身服务，为容器切换释放端口并确保数据落盘。
func (s *Server) shutdownForRecreate() {
	if s.http != nil {
		_ = s.http.Close()
	}
	_ = daemonpkg.RequestStop(s.app.DataDir)
	s.app.Shutdown()
}

// statusText 从 Docker API 错误响应里取可读的 message。
func statusText(status int, data []byte) string {
	var e struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &e) == nil && strings.TrimSpace(e.Message) != "" {
		return fmt.Sprintf("Docker 返回 %d：%s", status, e.Message)
	}
	return fmt.Sprintf("Docker 返回 %d", status)
}

// dockerArchTag 返回当前容器架构对应的镜像标签。
//
// 分架构镜像不再共用 latest（避免不同架构之间互相覆盖），改为：
// amd64-latest / arm64-latest / armv7-latest。latest 仅保留给 amd64 兼容旧部署。
func dockerArchTag() string {
	switch runtime.GOARCH {
	case "arm64":
		return "arm64-latest"
	case "arm":
		return "armv7-latest"
	default:
		return "amd64-latest"
	}
}
