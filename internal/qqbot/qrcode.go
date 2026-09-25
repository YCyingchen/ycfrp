package qqbot

import (
	"encoding/base64"
	"fmt"

	qrcode "github.com/skip2/go-qrcode"
)

// QRCodeDataURL 把扫码地址渲染成 PNG 二维码，并以 data URL 形式返回。
//
// 之所以返回 data URL 而不是文件路径：面板前端拿到就能直接塞进 img，
// 不需要额外的静态资源路由，也不会在磁盘留下临时图片。
func QRCodeDataURL(content string, size int) (string, error) {
	if size < 128 {
		size = 256
	}
	if size > 1024 {
		size = 1024
	}
	png, err := qrcode.Encode(content, qrcode.Medium, size)
	if err != nil {
		return "", fmt.Errorf("生成二维码失败：%w", err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png), nil
}

// QRCodePNG 返回二维码的原始 PNG 字节，供需要直接输出图片的场景使用。
func QRCodePNG(content string, size int) ([]byte, error) {
	if size < 128 {
		size = 256
	}
	png, err := qrcode.Encode(content, qrcode.Medium, size)
	if err != nil {
		return nil, fmt.Errorf("生成二维码失败：%w", err)
	}
	return png, nil
}
