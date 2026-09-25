package logx

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Level is a coarse severity ranking used by the panel log viewer.
type Level string

const (
	LevelTrace Level = "trace"
	LevelDebug Level = "debug"
	LevelInfo  Level = "info"
	LevelWarn  Level = "warn"
	LevelError Level = "error"
	LevelFatal Level = "fatal"
)

// LevelLabel returns the Chinese label rendered in the panel.
func LevelLabel(l Level) string {
	switch l {
	case LevelTrace:
		return "追踪"
	case LevelDebug:
		return "调试"
	case LevelInfo:
		return "信息"
	case LevelWarn:
		return "警告"
	case LevelError:
		return "错误"
	case LevelFatal:
		return "严重"
	default:
		return "信息"
	}
}

// LevelRank orders levels so the viewer can colour them consistently.
func LevelRank(l Level) int {
	switch l {
	case LevelTrace:
		return 0
	case LevelDebug:
		return 1
	case LevelInfo:
		return 2
	case LevelWarn:
		return 3
	case LevelError:
		return 4
	case LevelFatal:
		return 5
	default:
		return 2
	}
}

// Entry is a single log record.
type Entry struct {
	Time    time.Time `json:"time"`
	Level   Level     `json:"level"`
	Label   string    `json:"label"`
	Source  string    `json:"source"`
	Message string    `json:"message"`
	Raw     string    `json:"raw,omitempty"`
}

// Hint carries an actionable Chinese resolution suggestion for known failures.
type Hint struct {
	Pattern string `json:"pattern"`
	Title   string `json:"title"`
	Cause   string `json:"cause"`
	Fix     string `json:"fix"`
}

var hints = []Hint{
	{
		Pattern: "login to server failed",
		Title:   "连接服务端失败",
		Cause:   "frpc 无法使用当前令牌与 frps 建立控制连接，通常为 token 不一致或服务端端口不可达。",
		Fix:     "核对客户端与服务端 token 是否完全相同，并确认服务端地址与绑定端口可达。",
	},
	{
		Pattern: "authorization failed",
		Title:   "令牌校验未通过",
		Cause:   "两端 auth.token 不匹配，服务端拒绝了本次登录。",
		Fix:     "在面板「服务端」与「客户端」页面将 token 保持一致后重启内核。",
	},
	{
		Pattern: "port already used",
		Title:   "端口已被占用",
		Cause:   "隧道声明的远程端口已在本机或服务端被其他进程监听。",
		Fix:     "更换隧道远程端口，或结束占用该端口的进程后重新启动隧道。",
	},
	{
		Pattern: "connection refused",
		Title:   "连接被拒绝",
		Cause:   "目标地址上没有服务在监听，或防火墙拦截了该连接。",
		Fix:     "确认本地服务已启动并监听在隧道配置的本地端口上。",
	},
	{
		Pattern: "i/o timeout",
		Title:   "网络超时",
		Cause:   "与服务端之间的链路超时，可能是网络抖动、代理异常或服务端未启动。",
		Fix:     "检查网络与代理连通性，并确认 frps 服务端处于运行状态。",
	},
	{
		Pattern: "start error",
		Title:   "隧道启动失败",
		Cause:   "隧道参数未通过服务端校验，常见于端口越界或名称冲突。",
		Fix:     "检查隧道远程端口是否落在服务端允许的端口区间内。",
	},
	{
		Pattern: "no route to host",
		Title:   "目标主机不可达",
		Cause:   "路由或 DNS 解析异常，无法抵达服务端地址。",
		Fix:     "核对服务端地址拼写，并确认本机 DNS 与默认网关正常。",
	},
}

// Explain returns the first matching Chinese hint for a raw log line.
func Explain(line string) *Hint {
	lower := strings.ToLower(line)
	for i := range hints {
		if strings.Contains(lower, strings.ToLower(hints[i].Pattern)) {
			return &hints[i]
		}
	}
	return nil
}

// Logger keeps an in-memory ring buffer plus an on-disk archive of log lines.
type Logger struct {
	mu       sync.RWMutex
	entries  []Entry
	capacity int
	subs     map[int]chan Entry
	nextSub  int
	dir      string
	file     *os.File
	fileDate string
	minLevel Level
	cliLevel Level
}

// New creates a logger retaining the most recent capacity entries.
func New(capacity int) *Logger {
	if capacity <= 0 {
		capacity = 2000
	}
	return &Logger{
		entries:  make([]Entry, 0, capacity),
		capacity: capacity,
		subs:     make(map[int]chan Entry),
		minLevel: LevelInfo,
		cliLevel: LevelInfo,
	}
}

// SetDir enables daily-rotated file logging under dir.
func (l *Logger) SetDir(dir string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.dir = dir
	return os.MkdirAll(dir, 0o755)
}

// SetMinLevel controls which records are retained for the panel viewer.
func (l *Logger) SetMinLevel(level Level) {
	l.mu.Lock()
	l.minLevel = level
	l.mu.Unlock()
}

// SetConsoleLevel controls which records are echoed to the process stdout.
func (l *Logger) SetConsoleLevel(level Level) {
	l.mu.Lock()
	l.cliLevel = level
	l.mu.Unlock()
}

// Subscribe registers a listener for live log streaming.
func (l *Logger) Subscribe() (int, <-chan Entry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	id := l.nextSub
	l.nextSub++
	ch := make(chan Entry, 256)
	l.subs[id] = ch
	return id, ch
}

// Unsubscribe removes a previously registered listener.
func (l *Logger) Unsubscribe(id int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if ch, ok := l.subs[id]; ok {
		close(ch)
		delete(l.subs, id)
	}
}

// Log records one entry derived from a raw line.
func (l *Logger) Log(level Level, source, message string) {
	entry := Entry{
		Time:    time.Now(),
		Level:   level,
		Label:   LevelLabel(level),
		Source:  source,
		Message: message,
	}
	l.append(entry)
}

// LogRaw records a raw kernel line, auto-detecting its severity.
func (l *Logger) LogRaw(source, line string) {
	line = strings.TrimRight(line, "\r\n")
	if strings.TrimSpace(line) == "" {
		return
	}
	entry := Entry{
		Time:    time.Now(),
		Level:   detectLevel(line),
		Source:  source,
		Raw:     line,
		Message: cleanKernelMessage(line),
	}
	entry.Label = LevelLabel(entry.Level)
	l.append(entry)
}

func detectLevel(line string) Level {
	lower := strings.ToLower(line)
	switch {
	case strings.Contains(lower, "[e") && strings.Contains(lower, "error"):
		return LevelError
	case strings.Contains(lower, "error"), strings.Contains(lower, "failed"),
		strings.Contains(lower, "panic"), strings.Contains(lower, "fatal"):
		return LevelError
	case strings.Contains(lower, "[w") && strings.Contains(lower, "warn"):
		return LevelWarn
	case strings.Contains(lower, "warn"):
		return LevelWarn
	case strings.Contains(lower, "[d") && strings.Contains(lower, "debug"):
		return LevelDebug
	case strings.Contains(lower, "[t") && strings.Contains(lower, "trace"):
		return LevelTrace
	default:
		return LevelInfo
	}
}

func normalize(line string) string {
	// Strip the leading timestamp emitted by the kernel so the viewer shows a
	// stable, readable message while the raw line stays intact in Raw.
	if len(line) > 20 && line[0] >= '0' && line[0] <= '9' && strings.Contains(line[:20], ":") {
		if idx := strings.IndexByte(line, ' '); idx > 0 {
			rest := strings.TrimSpace(line[idx+1:])
			if rest != "" {
				return rest
			}
		}
	}
	return line
}

func (l *Logger) append(entry Entry) {
	l.mu.Lock()
	if LevelRank(entry.Level) < LevelRank(l.minLevel) {
		l.mu.Unlock()
		return
	}
	l.entries = append(l.entries, entry)
	if len(l.entries) > l.capacity {
		keep := l.capacity - l.capacity/8
		l.entries = append(l.entries[:0], l.entries[len(l.entries)-keep:]...)
	}
	console := LevelRank(entry.Level) >= LevelRank(l.cliLevel)
	subs := make([]chan Entry, 0, len(l.subs))
	for _, ch := range l.subs {
		subs = append(subs, ch)
	}
	l.mu.Unlock()

	if console {
		fmt.Printf("%s [%s] %s\n", entry.Time.Format("15:04:05"), entry.Label, entry.Message)
	}
	for _, ch := range subs {
		select {
		case ch <- entry:
		default:
		}
	}
	l.writeFile(entry)
}

func (l *Logger) writeFile(entry Entry) {
	l.mu.Lock()
	dir := l.dir
	l.mu.Unlock()
	if dir == "" {
		return
	}
	date := entry.Time.Format("2006-01-02")
	l.mu.Lock()
	if l.fileDate != date || l.file == nil {
		if l.file != nil {
			_ = l.file.Close()
		}
		_ = os.MkdirAll(dir, 0o755)
		f, err := os.OpenFile(filepath.Join(dir, "ycfrp-"+date+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			l.mu.Unlock()
			return
		}
		l.file = f
		l.fileDate = date
	}
	f := l.file
	l.mu.Unlock()
	if f == nil {
		return
	}
	_, _ = f.WriteString(fmt.Sprintf("%s [%s] %s\n", entry.Time.Format("2006-01-02 15:04:05"), entry.Label, entry.Message))
}

// Query returns log entries filtered by level and keyword, newest last.
func (l *Logger) Query(minLevel Level, keyword string, limit int) []Entry {
	l.mu.RLock()
	defer l.mu.RUnlock()
	keyword = strings.ToLower(strings.TrimSpace(keyword))
	out := make([]Entry, 0, len(l.entries))
	for i := len(l.entries) - 1; i >= 0; i-- {
		e := l.entries[i]
		if LevelRank(e.Level) < LevelRank(minLevel) {
			continue
		}
		if keyword != "" {
			hay := strings.ToLower(e.Message + " " + e.Raw + " " + e.Source)
			if !strings.Contains(hay, keyword) {
				continue
			}
		}
		out = append(out, e)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	// Present newest first for the panel.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	return out
}

// Analyze groups problematic entries with their Chinese explanations.
func (l *Logger) Analyze(limit int) []map[string]any {
	l.mu.RLock()
	defer l.mu.RUnlock()
	type bucket struct {
		count int
		last  time.Time
		sample string
	}
	agg := make(map[string]*bucket)
	for i := len(l.entries) - 1; i >= 0; i-- {
		e := l.entries[i]
		if LevelRank(e.Level) < LevelRank(LevelWarn) {
			continue
		}
		h := Explain(e.Message + " " + e.Raw)
		if h == nil {
			continue
		}
		b, ok := agg[h.Pattern]
		if !ok {
			b = &bucket{last: e.Time, sample: e.Message}
			agg[h.Pattern] = b
		}
		b.count++
	}
	out := make([]map[string]any, 0, len(agg))
	for _, h := range hints {
		b, ok := agg[h.Pattern]
		if !ok {
			continue
		}
		out = append(out, map[string]any{
			"title":  h.Title,
			"cause":  h.Cause,
			"fix":    h.Fix,
			"count":  b.count,
			"last":   b.last,
			"sample": b.sample,
		})
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// Export renders all retained entries as a human readable text document.
func (l *Logger) Export() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	var sb strings.Builder
	sb.WriteString("YCFRP 运行日志导出\n")
	sb.WriteString("导出时间: " + time.Now().Format("2006-01-02 15:04:05") + "\n")
	sb.WriteString(strings.Repeat("=", 72) + "\n\n")
	for _, e := range l.entries {
		sb.WriteString(fmt.Sprintf("%s [%s] (%s) %s\n",
			e.Time.Format("2006-01-02 15:04:05"), e.Label, e.Source, e.Message))
		if e.Raw != "" && e.Raw != e.Message {
			sb.WriteString("    原始: " + e.Raw + "\n")
		}
	}
	analysis := l.analyzeLocked()
	if len(analysis) > 0 {
		sb.WriteString("\n" + strings.Repeat("=", 72) + "\n")
		sb.WriteString("问题定位\n\n")
		for _, item := range analysis {
			sb.WriteString(fmt.Sprintf("· %v（出现 %v 次）\n", item["title"], item["count"]))
			sb.WriteString(fmt.Sprintf("  原因: %v\n", item["cause"]))
			sb.WriteString(fmt.Sprintf("  建议: %v\n\n", item["fix"]))
		}
	}
	return sb.String()
}

func (l *Logger) analyzeLocked() []map[string]any {
	type bucket struct {
		count int
		last  time.Time
		sample string
	}
	agg := make(map[string]*bucket)
	for i := len(l.entries) - 1; i >= 0; i-- {
		e := l.entries[i]
		if LevelRank(e.Level) < LevelRank(LevelWarn) {
			continue
		}
		h := Explain(e.Message + " " + e.Raw)
		if h == nil {
			continue
		}
		b, ok := agg[h.Pattern]
		if !ok {
			b = &bucket{last: e.Time, sample: e.Message}
			agg[h.Pattern] = b
		}
		b.count++
	}
	out := make([]map[string]any, 0, len(agg))
	for _, h := range hints {
		b, ok := agg[h.Pattern]
		if !ok {
			continue
		}
		out = append(out, map[string]any{
			"title": h.Title, "cause": h.Cause, "fix": h.Fix,
			"count": b.count, "last": b.last, "sample": b.sample,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i]["count"].(int) > out[j]["count"].(int)
	})
	return out
}

// Capture redirects the process stdout and stderr into this logger. It is used
// to fold the embedded frp kernel output into the unified Chinese log view.
func (l *Logger) Capture(source string) (func(), error) {
	origOut, origErr := os.Stdout, os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	os.Stdout = w
	os.Stderr = w

	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 0, 4096)
		tmp := make([]byte, 1024)
		for {
			n, err := r.Read(tmp)
			if n > 0 {
				buf = append(buf, tmp[:n]...)
				for {
					idx := indexByte(buf, '\n')
					if idx < 0 {
						break
					}
					line := string(buf[:idx])
					buf = buf[idx+1:]
					l.LogRaw(source, line)
				}
			}
			if err != nil {
				if len(buf) > 0 {
					l.LogRaw(source, string(buf))
				}
				return
			}
		}
	}()

	return func() {
		os.Stdout, os.Stderr = origOut, origErr
		_ = w.Close()
		<-done
		_ = r.Close()
	}, nil
}

func indexByte(b []byte, c byte) int {
	for i := range b {
		if b[i] == c {
			return i
		}
	}
	return -1
}

// Write implements io.Writer so the logger can be plugged into standard sinks.
func (l *Logger) Write(p []byte) (int, error) {
	for _, line := range strings.Split(string(p), "\n") {
		if strings.TrimSpace(line) != "" {
			l.LogRaw("kernel", line)
		}
	}
	return len(p), nil
}

var _ io.Writer = (*Logger)(nil)
