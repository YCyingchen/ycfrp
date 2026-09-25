//go:build !windows

package logx

// EnableConsoleEncoding 在类 Unix 系统上是空操作：终端一律按 UTF-8 解释，
// 程序输出本身就是 UTF-8，无需转换。
func EnableConsoleEncoding() {}

// FlushConsole 在类 Unix 系统上是空操作。
func FlushConsole() {}
