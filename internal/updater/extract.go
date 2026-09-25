package updater

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// maxExtractBytes 限制单个文件解压后的体积，避免压缩炸弹把磁盘写满。
const maxExtractBytes = 200 << 20

// ExtractBinary 从安装包里取出可执行文件并落到 destFile。
//
// 支持 Linux 的 .tar.gz 与 Windows 的 .zip。归档里可能同时含多个可执行文件
// （Windows 包有 GUI 版与控制台版），因此按 preferName 优先挑选与当前运行的
// 程序同名的那个，找不到时再回退到归档中第一个可执行文件。
func ExtractBinary(archivePath, destFile, preferName string) (string, error) {
	lower := strings.ToLower(archivePath)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return extractFromZip(archivePath, destFile, preferName)
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return extractFromTarGz(archivePath, destFile, preferName)
	default:
		// 直接上传裸二进制也允许：当作可执行文件原样落盘。
		if err := copyFile(archivePath, destFile, 0o755); err != nil {
			return "", err
		}
		return destFile, nil
	}
}

func extractFromZip(archivePath, destFile, preferName string) (string, error) {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return "", fmt.Errorf("打开压缩包失败：%w", err)
	}
	defer zr.Close()

	var chosen *zip.File
	for i := range zr.File {
		f := zr.File[i]
		if f.FileInfo().IsDir() || !isBinaryName(f.Name) {
			continue
		}
		if chosen == nil {
			chosen = f
		}
		if preferName != "" && strings.EqualFold(filepath.Base(f.Name), preferName) {
			chosen = f
			break
		}
	}
	if chosen == nil {
		return "", fmt.Errorf("压缩包里没有找到可执行文件")
	}
	rc, err := chosen.Open()
	if err != nil {
		return "", fmt.Errorf("读取压缩包内文件失败：%w", err)
	}
	defer rc.Close()
	return writeLimited(rc, destFile)
}

func extractFromTarGz(archivePath, destFile, preferName string) (string, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return "", fmt.Errorf("打开压缩包失败：%w", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", fmt.Errorf("解压失败：%w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	var fallback *tar.Header
	var fallbackData []byte
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("读取压缩包失败：%w", err)
		}
		if hdr.Typeflag != tar.TypeReg || !isBinaryName(hdr.Name) {
			continue
		}
		base := filepath.Base(hdr.Name)
		if preferName != "" && strings.EqualFold(base, preferName) {
			return writeLimited(tr, destFile)
		}
		// 先缓存第一个候选，万一后面没有同名文件就用它。
		if fallback == nil {
			data, err := io.ReadAll(io.LimitReader(tr, maxExtractBytes))
			if err != nil {
				return "", fmt.Errorf("读取压缩包内文件失败：%w", err)
			}
			fallback = hdr
			fallbackData = data
		}
	}
	if fallback == nil {
		return "", fmt.Errorf("压缩包里没有找到可执行文件")
	}
	return writeBytes(fallbackData, destFile)
}

// isBinaryName 判断归档条目是否是面板可执行文件：Linux 下是 ycfrp，
// Windows 下是 *.exe；其余脚本与说明文件一律跳过。
func isBinaryName(name string) bool {
	base := strings.ToLower(filepath.Base(name))
	if base == "" || strings.HasPrefix(base, ".") {
		return false
	}
	if strings.HasSuffix(base, ".exe") {
		return true
	}
	return base == "ycfrp"
}

func writeLimited(r io.Reader, destFile string) (string, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxExtractBytes))
	if err != nil {
		return "", fmt.Errorf("读取可执行文件失败：%w", err)
	}
	return writeBytes(data, destFile)
}

func writeBytes(data []byte, destFile string) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("可执行文件内容为空")
	}
	if err := os.MkdirAll(filepath.Dir(destFile), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(destFile, data, 0o755); err != nil {
		return "", fmt.Errorf("写入可执行文件失败：%w", err)
	}
	// Windows 会忽略权限位，Linux 上必须显式给可执行权限。
	_ = os.Chmod(destFile, 0o755)
	return destFile, nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("打开文件失败：%w", err)
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("创建文件失败：%w", err)
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("复制文件失败：%w", err)
	}
	_ = os.Chmod(dst, mode)
	return nil
}
