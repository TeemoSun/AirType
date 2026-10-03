//go:build windows

package ui

import (
	"sync"

	"github.com/lxn/walk"
	"golang.org/x/sys/windows/registry"

	"github.com/TeemoSun/AirType/internal/win"
)

// Palette 是界面配色 token（浅色/深色两套），按 Win11 设计规范校准：
// 中性底 + 卡片分层 + 描边 + 系统强调色点睛，实色呈现（无材质依赖，
// Win10 同样成立）。
// 文字对比度按 WCAG AA 校准：正文 ≥7:1，次级 ≥4.5:1。
type Palette struct {
	Window        walk.Color // 窗口底色
	Card          walk.Color // 卡片/控件底（通道卡片、二维码白卡）
	Stroke        walk.Color // 描边/分隔线
	Text          walk.Color // 正文
	TextSecondary walk.Color // 次级文字/时间戳/提示
	Hover         walk.Color // 列表行悬停
	Selection     walk.Color // 键盘选中行（强调色低透明度预混，见 accentPalette）
	Pressed       walk.Color // 卡片按压底
	ScrollThumb   walk.Color // 滚动条滑块
	Flash         walk.Color // "已复制/新消息"反馈文字
	Accent        walk.Color // 系统强调色（卡片悬停描边等点睛处）
}

var paletteLight = Palette{
	Window:        walk.RGB(243, 243, 243), // #F3F3F3（Win11 浅色窗口底）
	Card:          walk.RGB(255, 255, 255),
	Stroke:        walk.RGB(225, 225, 225), // rgba(0,0,0,0.08) 等效
	Text:          walk.RGB(27, 27, 27),    // #1B1B1B
	TextSecondary: walk.RGB(96, 96, 96),    // rgba(0,0,0,0.6) 等效
	Hover:         walk.RGB(232, 232, 232),
	Selection:     walk.Color(0), // 由 accentPalette 按强调色预混填充
	Pressed:       walk.RGB(216, 216, 216),
	ScrollThumb:   walk.RGB(184, 184, 184),
	Flash:         walk.RGB(22, 101, 52),
}

var paletteDark = Palette{
	Window:        walk.RGB(32, 32, 32),    // #202020
	Card:          walk.RGB(44, 44, 44),    // #2C2C2C
	Stroke:        walk.RGB(62, 62, 62),    // rgba(255,255,255,0.1) 等效
	Text:          walk.RGB(232, 232, 232), // rgba(255,255,255,0.894) 等效
	TextSecondary: walk.RGB(166, 166, 166), // rgba(255,255,255,0.6) 等效
	Hover:         walk.RGB(47, 47, 47),
	Selection:     walk.Color(0),
	Pressed:       walk.RGB(58, 58, 58),
	ScrollThumb:   walk.RGB(106, 106, 106),
	Flash:         walk.RGB(134, 220, 165),
}

var themeMu sync.Mutex

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

// readRegDword 读 HKCU 下的 DWORD 值。
func readRegDword(keyPath, value string) (uint32, bool) {
	k, err := registry.OpenKey(registry.CURRENT_USER, keyPath, registry.QUERY_VALUE)
	if err != nil {
		return 0, false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue(value)
	if err != nil {
		return 0, false
	}
	return uint32(v), true
}

// systemAccent 读用户强调色（设置 → 个性化 → 颜色）。AccentColorMenu 优先
// （0xAABBGGRR，取低 24 位即 COLORREF），回退 DWM ColorizationColor；都
// 读不到给 Windows 默认蓝。
func systemAccent() walk.Color {
	if v, ok := readRegDword(
		`Software\Microsoft\Windows\CurrentVersion\Explorer\Accent`, "AccentColorMenu"); ok {
		return walk.Color(v & 0xFFFFFF)
	}
	if v, ok := readRegDword(`Software\Microsoft\Windows\DWM`, "ColorizationColor"); ok {
		return walk.Color(v & 0xFFFFFF)
	}
	return walk.RGB(0, 120, 215)
}

// mix 把 b 以权重 t（0~1）混入 a。
func mix(a, b walk.Color, t float64) walk.Color {
	ar, ag, ab := float64(a.R()), float64(a.G()), float64(a.B())
	br, bg, bb := float64(b.R()), float64(b.G()), float64(b.B())
	return walk.RGB(uint8(ar+(br-ar)*t+0.5), uint8(ag+(bg-ag)*t+0.5), uint8(ab+(bb-ab)*t+0.5))
}

// accentPalette 填充依赖强调色的派生 token：选中行 = 强调色 8% 预混窗口底
// （实色底上预混与半透明叠加观感一致，且省一次 alpha 合成）。
func accentPalette(p Palette) Palette {
	accent := systemAccent()
	p.Selection = mix(p.Window, accent, 0.08)
	return p
}

// currentPalette 返回当前配色与是否深色（深色时窗口须染深色标题栏）。
// 每次都重读注册表（只在创建/打开窗口时调用，开销可忽略）：
// 运行中切换系统深浅色，下一个打开的窗口即跟随新主题。
func currentPalette() (Palette, bool) {
	themeMu.Lock()
	defer themeMu.Unlock()
	if systemDark() {
		return accentPalette(paletteDark), true
	}
	return accentPalette(paletteLight), false
}

// 字体族：Win11 优先 Segoe UI Variable（Display 用于标题，光学尺寸更紧
// 凑），Win10/未装回退 Segoe UI。按系统版本判断，不做字体探测。
var (
	fontOnce        sync.Once
	uiFontFamilyV   string
	displayFontFamV string
)

func initFontFamilies() {
	fontOnce.Do(func() {
		if win.IsWin11() {
			uiFontFamilyV = "Segoe UI Variable Text"
			displayFontFamV = "Segoe UI Variable Display"
		} else {
			uiFontFamilyV = "Segoe UI"
			displayFontFamV = "Segoe UI"
		}
	})
}

// UIFontFamily 正文/控件字体族。
func UIFontFamily() string {
	initFontFamilies()
	return uiFontFamilyV
}

// DisplayFontFamily 标题字体族。
func DisplayFontFamily() string {
	initFontFamilies()
	return displayFontFamV
}
