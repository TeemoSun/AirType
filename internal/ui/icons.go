//go:build windows

package ui

import (
	"embed"
	"image/png"

	"github.com/lxn/walk"
)

//go:embed icons/green.png icons/yellow.png icons/red.png icons/gray.png
var iconFS embed.FS

// stateIcon 加载状态图标：green=正常、yellow=需要注意、red=出错、gray=未绑定。
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
