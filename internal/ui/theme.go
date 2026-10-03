//go:build windows

package ui

import (
	"sync"

	"github.com/lxn/walk"
	"golang.org/x/sys/windows/registry"
)

// Palette 是界面配色 token（浅色/深色两套）。
// 文字对比度按 WCAG AA 校准：正文 ≥7:1，次级 ≥4.5:1。
type Palette struct {
	Window        walk.Color // 窗口底色
	Text          walk.Color // 正文
	TextSecondary walk.Color // 次级文字/时间戳/提示
	Hover         walk.Color // 列表行悬停
	Selection     walk.Color // 键盘选中行
	ScrollThumb   walk.Color // 滚动条滑块
	Flash         walk.Color // "已复制/新消息"反馈文字
}

var paletteLight = Palette{
	Window:        walk.RGB(255, 255, 255),
	Text:          walk.RGB(26, 28, 32),
	TextSecondary: walk.RGB(93, 93, 93),
	Hover:         walk.RGB(239, 239, 239),
	Selection:     walk.RGB(233, 235, 238),
	ScrollThumb:   walk.RGB(198, 198, 198),
	Flash:         walk.RGB(22, 101, 52),
}

var paletteDark = Palette{
	Window:        walk.RGB(32, 32, 32),
	Text:          walk.RGB(240, 240, 240),
	TextSecondary: walk.RGB(158, 158, 158),
	Hover:         walk.RGB(51, 51, 51),
	Selection:     walk.RGB(62, 62, 62),
	ScrollThumb:   walk.RGB(112, 112, 112),
	Flash:         walk.RGB(134, 220, 165),
}

var (
	themeMu    sync.Mutex
	themeDark  bool
	themeKnown bool
)

// systemDark 读系统应用主题（AppsUseLightTheme=0 即深色）；读不到按浅色。
func systemDark() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("AppsUseLightTheme")
	if err != nil {
		return false
	}
	return v == 0
}

// currentPalette 返回当前配色与是否深色（深色时窗口须染深色标题栏）。
// 首次调用探测并缓存；ensureXWindow 在创建窗口前调用以跟随系统切换。
func currentPalette() (Palette, bool) {
	themeMu.Lock()
	defer themeMu.Unlock()
	if !themeKnown {
		themeDark, themeKnown = systemDark(), true
	}
	if themeDark {
		return paletteDark, true
	}
	return paletteLight, false
}
