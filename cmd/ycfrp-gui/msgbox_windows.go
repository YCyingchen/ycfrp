//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

var (
	user32DLL      = syscall.NewLazyDLL("user32.dll")
	procMessageBox = user32DLL.NewProc("MessageBoxW")
)

// messageBox shows a modal Windows message box. It is used instead of console
// output because the GUI build is linked with -H windowsgui and has no console.
func messageBox(title, text string, flags uintptr) {
	titlePtr, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return
	}
	textPtr, err := syscall.UTF16PtrFromString(text)
	if err != nil {
		return
	}
	procMessageBox.Call(
		0,
		uintptr(unsafe.Pointer(textPtr)),
		uintptr(unsafe.Pointer(titlePtr)),
		flags,
	)
}
