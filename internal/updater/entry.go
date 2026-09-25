package updater

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// MaybeRunHelper 在程序启动的最开始判断自己是否是「更新助手」。若是，则执行
// 替换流程并返回退出码；否则返回 handled=false，交回正常启动逻辑。
//
// 助手进程由面板派生，没有控制台也没有父进程可读取输出，因此它的日志统一
// 追加到数据目录下的 ycfrp-update.log，方便排查更新失败的原因。
func MaybeRunHelper(args []string) (int, bool) {
	if !IsHelperInvocation(args) {
		return 0, false
	}
	req, err := ParseApplyRequest(args)
	logf := func(format string, a ...any) {
		writeHelperLog(req.DataDir, fmt.Sprintf(format, a...))
	}
	if err != nil {
		logf("解析更新参数失败：%v", err)
		return 1, true
	}
	code := RunApply(req, logf)
	// 助手用的临时目录在启动时创建，用完即清，避免长期堆积。
	if self, err := os.Executable(); err == nil {
		_ = os.RemoveAll(filepath.Dir(self))
	}
	return code, true
}

func writeHelperLog(dataDir, msg string) {
	line := fmt.Sprintf("%s %s\n", time.Now().Format("2006-01-02 15:04:05"), msg)
	if dataDir == "" {
		return
	}
	dir := filepath.Join(dataDir, "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "ycfrp-update.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(line)
}
