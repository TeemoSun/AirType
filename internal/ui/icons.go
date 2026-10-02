//go:build windows

package ui

import (
	"embed"
	"image/png"

	"github.com/lxn/walk"
)

//go:embed icons/green.png icons/gray.png icons/red.png
var iconFS embed.FS

// stateIcon 加载状态图标：green=已连接、gray=暂停、red=断开/需扫码。
func stateIcon(name string) (*walk.Icon, error) {
	f, err := iconFS.Open("icons/" + name + ".png")
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
