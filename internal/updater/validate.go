package updater

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"runtime"
)

// ELF 与 PE 的机器码常量，用于确认待替换的二进制与当前运行环境架构一致。
// 这与 Dockerfile 构建期的校验保持一致，避免把错架构的包装进面板。
const (
	elfMagic0 = 0x7F
	elfMachineAMD64 = 62
	elfMachineARM64 = 183
	elfMachineARM   = 40

	peMachineAMD64 = 0x8664
	peMachineARM64 = 0xAA64
	peMachineARM   = 0x01C4
)

// VerifyBinary 校验待安装的二进制是否是可执行文件，且在架构上与当前进程匹配。
// 校验失败时返回中文原因，让面板能直接给出可读提示，而不是在替换后才失败。
func VerifyBinary(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("无法打开待安装文件：%w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("无法读取待安装文件信息：%w", err)
	}
	if info.Size() == 0 {
		return fmt.Errorf("待安装文件为空")
	}

	head := make([]byte, 512)
	n, _ := f.Read(head)
	head = head[:n]
	if n < 64 {
		return fmt.Errorf("待安装文件不完整")
	}

	switch {
	case head[0] == elfMagic0 && head[1] == 'E' && head[2] == 'L' && head[3] == 'F':
		return verifyELF(head)
	case head[0] == 'M' && head[1] == 'Z':
		return verifyPE(f, head)
	default:
		return fmt.Errorf("待安装文件不是可执行的程序（既不是 ELF 也不是 PE 格式）")
	}
}

func verifyELF(head []byte) error {
	if len(head) < 20 {
		return fmt.Errorf("ELF 文件头不完整")
	}
	machine := binary.LittleEndian.Uint16(head[18:20])
	if machine != expectedELF() {
		return fmt.Errorf("待安装程序架构不匹配：期望 %s，实际机器码 %d",
			runtime.GOARCH, machine)
	}
	return nil
}

func expectedELF() uint16 {
	switch runtime.GOARCH {
	case "amd64":
		return elfMachineAMD64
	case "arm64":
		return elfMachineARM64
	case "arm":
		return elfMachineARM
	default:
		return 0
	}
}

func verifyPE(f *os.File, head []byte) error {
	if len(head) < 64 {
		return fmt.Errorf("PE 文件头不完整")
	}
	offset := binary.LittleEndian.Uint32(head[0x3C:0x40])
	if offset == 0 || offset > 1<<20 {
		return fmt.Errorf("PE 文件头偏移异常")
	}
	sig := make([]byte, 6)
	if _, err := f.ReadAt(sig, int64(offset)); err != nil {
		return fmt.Errorf("无法读取 PE 头：%w", err)
	}
	if !bytes.Equal(sig[:4], []byte{'P', 'E', 0, 0}) {
		return fmt.Errorf("PE 签名不正确")
	}
	machine := binary.LittleEndian.Uint16(sig[4:6])
	if machine != expectedPE() {
		return fmt.Errorf("待安装程序架构不匹配：期望 %s，实际机器码 0x%X",
			runtime.GOARCH, machine)
	}
	return nil
}

func expectedPE() uint16 {
	switch runtime.GOARCH {
	case "amd64":
		return peMachineAMD64
	case "arm64":
		return peMachineARM64
	case "arm":
		return peMachineARM
	default:
		return 0
	}
}
