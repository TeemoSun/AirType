//go:build windows

package ui

import (
	"embed"
	"image/png"

	"github.com/lxn/walk"
)

//go:embed icons/green.png icons/yellow.png icons/red.png
var iconFS embed.FS

// stateIcon 加载状态图标：green=一切正常、yellow=连接异常、red=未登录/需扫码。
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
