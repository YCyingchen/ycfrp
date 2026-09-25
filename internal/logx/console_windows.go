//go:build windows

package logx

import (
	"io"
	"os"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/transform"
)

var (
	consoleOnce sync.Once
	consolePipe *os.File
	consoleDone chan struct{}
	kernel32    = syscall.NewLazyDLL("kernel32.dll")
	procGetCP   = kernel32.NewProc("GetConsoleOutputCP")
)

// EnableConsoleEncoding 让中文输出在当前控制台代码页下也能正确显示。
//
// Go 的 fmt.Printf 一律输出 UTF-8，而中文 Windows 的控制台默认是 GBK(936)
// 代码页，直接把 UTF-8 字节打到控制台就会变成乱码。这里在真实控制台上把
// os.Stdout/os.Stderr 换成管道，后台把 UTF-8 转成控制台代码页再输出；
// 输出被重定向到文件（非控制台）时保持原样，避免污染日志内容。
//
// 可用 YCFRP_CONSOLE_CHARSET 覆盖：gbk / utf8 / auto（默认）。
func EnableConsoleEncoding() {
	consoleOnce.Do(func() {
		if !isConsole(os.Stdout.Fd()) {
			// 非控制台（重定向到文件或管道）：仅在被明确要求时转码。
			if strings.EqualFold(os.Getenv("YCFRP_CONSOLE_CHARSET"), "gbk") {
				wrapConsoleOutput(simplifiedchinese.GBK.NewEncoder)
			}
			return
		}
		switch strings.ToLower(strings.TrimSpace(os.Getenv("YCFRP_CONSOLE_CHARSET"))) {
		case "utf8", "utf-8", "none":
			return
		case "gbk":
			wrapConsoleOutput(simplifiedchinese.GBK.NewEncoder)
			return
		}
		wrapConsoleOutput(encoderForCodePage)
	})
}

// FlushConsole 关闭转码管道并等待后台写入完成。程序退出前调用，确保最后
// 几行提示不会因为进程结束而丢失。
func FlushConsole() {
	if consolePipe == nil {
		return
	}
	file := consolePipe
	consolePipe = nil
	_ = file.Close()
	if consoleDone != nil {
		<-consoleDone
	}
}

func wrapConsoleOutput(pick func() *encoding.Encoder) {
	var enc *encoding.Encoder
	if pick != nil {
		enc = pick()
	}
	if enc == nil {
		return
	}

	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		return
	}

	consolePipe = w
	consoleDone = make(chan struct{})
	os.Stdout = w
	os.Stderr = w

	go func() {
		defer close(consoleDone)
		_, _ = io.Copy(orig, transform.NewReader(r, enc))
		_ = r.Close()
	}()
}

// encoderForCodePage 按当前控制台代码页选择编码器，不支持时返回 nil 表示不转码。
func encoderForCodePage() *encoding.Encoder {
	cp, _, _ := procGetCP.Call()
	switch uint32(cp) {
	case 936:
		return simplifiedchinese.GBK.NewEncoder()
	case 950:
		return traditionalchinese.Big5.NewEncoder()
	}
	return nil
}

// isConsole 报告句柄是否指向真实控制台。
func isConsole(fd uintptr) bool {
	var mode uint32
	return syscall.GetConsoleMode(syscall.Handle(fd), &mode) == nil
}
