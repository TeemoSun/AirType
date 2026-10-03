//go:build windows

package ui

import (
	"embed"
	"image/png"

	"github.com/lxn/walk"
)

// 四态 256px 主图 + 托盘用 16/24/32px 变体（由 cmd/icongen 生成）。
//
//go:embed icons/green.png icons/yellow.png icons/red.png icons/gray.png icons/green-16.png icons/yellow-16.png icons/red-16.png icons/gray-16.png icons/green-24.png icons/yellow-24.png icons/red-24.png icons/gray-24.png icons/green-32.png icons/yellow-32.png icons/red-32.png icons/gray-32.png
var iconFS embed.FS

// stateIcon 按托盘 DPI 加载状态图标：托盘逻辑尺寸 16px，按 DPI 选最接近
// 的预渲染变体（16/24/32 物理像素 1:1 显示，避免 256px 一次性缩糊）。
// 语义：green=正常、yellow=需要注意、red=出错、gray=未绑定。
func stateIcon(name string, dpi int) (*walk.Icon, error) {
	p := 16 * dpi / 96
	variant := "-32"
	if p <= 18 {
		variant = "-16"
	} else if p <= 27 {
		variant = "-24"
	}
	f, err := iconFS.Open("icons/" + name + variant + ".png")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return nil, err
	}
	return walk.NewIconFromImage(img)
}
