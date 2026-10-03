//go:build windows

package win

import (
	"syscall"
	"unsafe"
)

var (
	dwmapi                        = syscall.NewLazyDLL("dwmapi.dll")
	pDwmSetWindowAttribute        = dwmapi.NewProc("DwmSetWindowAttribute")
	pDwmExtendFrameIntoClientArea = dwmapi.NewProc("DwmExtendFrameIntoClientArea")
	pSetWindowPos                 = user32.NewProc("SetWindowPos")
	pSetWindowTheme               = syscall.NewLazyDLL("uxtheme.dll").NewProc("SetWindowTheme")
)

// DWM 颜色类属性的特殊值（DWMWA_CAPTION_COLOR / DWMWA_BORDER_COLOR）。
const (
	DwmColorDefault uint32 = 0xFFFFFFFF // 恢复系统默认
	DwmColorNone    uint32 = 0xFFFFFFFE // 无色（去掉描边）
)

// setWindowColorAttr 写 DWM 颜色类属性（COLORREF 0x00BBGGRR，与 walk.Color
// 同布局）。Win11 22000+ 支持；旧系统调用失败被忽略，无副作用。
func setWindowColorAttr(hwnd uintptr, attr, color uint32) {
	if hwnd == 0 {
		return
	}
	pDwmSetWindowAttribute.Call(hwnd, uintptr(attr), uintptr(unsafe.Pointer(&color)), 4)
}

// SetWindowCaptionColor 染标题栏底色（属性 35，Win11 22000+）。
// 与窗口底同色后标题栏"融进"窗口，是去掉老对话框感的关键一步。
func SetWindowCaptionColor(hwnd uintptr, color uint32) {
	setWindowColorAttr(hwnd, 35, color)
}

// SetWindowBorderColor 染窗口外描边（属性 34，Win11 22000+）。
func SetWindowBorderColor(hwnd uintptr, color uint32) {
	setWindowColorAttr(hwnd, 34, color)
}

// SetWindowNoBorder 去掉窗口 1px 系统描边。
func SetWindowNoBorder(hwnd uintptr) {
	setWindowColorAttr(hwnd, 34, DwmColorNone)
}

// SetWindowTextColor 染标题栏文字（属性 36，Win11 22000+）。
func SetWindowTextColor(hwnd uintptr, color uint32) {
	setWindowColorAttr(hwnd, 36, color)
}

// EnableSystemBackdrop 启用系统级窗口材质：主窗用 Mica、瞬态窗用 Acrylic
// （DWMWA_SYSTEMBACKDROP_TYPE=38，Win11 22H2+），深浅色自动跟随系统。
// 前置：客户区可见像素必须由保留 alpha 的通道绘制（本项目为 Direct2D）；
// GDI 绘制的像素 alpha=0，在材质上会变透明。未满足前置时画面不可控，
// 调用方应只在与用户可见内容全部走 D2D 的窗口上启用。
// 返回是否成功（版本不够/属性被拒返回 false，调用方回退实色主题）。
func EnableSystemBackdrop(hwnd uintptr, transient bool) bool {
	if hwnd == 0 || !HasSystemBackdrop() {
		return false
	}
	const dwmwaSystemBackdrop = 38
	kind := uint32(2) // DWMSBT_MAINWINDOW → Mica
	if transient {
		kind = 3 // DWMSBT_TRANSIENTWINDOW → Acrylic
	}
	hr, _, _ := pDwmSetWindowAttribute.Call(hwnd, uintptr(dwmwaSystemBackdrop),
		uintptr(unsafe.Pointer(&kind)), 4)
	if hr != 0 {
		return false
	}
	// 材质只在 DWM 扩展框架区域内透出：margins 全 -1 = 整个客户区
	margins := [4]int32{-1, -1, -1, -1}
	pDwmExtendFrameIntoClientArea.Call(hwnd, uintptr(unsafe.Pointer(&margins)))
	return true
}

// SetWindowThemeDark 让原生控件（按钮等）改用深色主题渲染
// （uxtheme "DarkMode_Explorer"，Win10 1809+；旧系统调用无害失败）。
// 深色模式下自绘窗口底是深色，原生按钮若留在浅色皮肤会变成刺眼白块。
func SetWindowThemeDark(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	cls, _ := syscall.UTF16PtrFromString("DarkMode_Explorer")
	_, _, _ = pSetWindowTheme.Call(hwnd, uintptr(unsafe.Pointer(cls)), 0)
}

// MakeBorderlessRoundedPopup 把窗口变成 Win11 风格无边框圆角浮层：
// 去掉标题栏/粗边框，强制 DWM 圆角。Win10 上圆角调用无害失败。
func MakeBorderlessRoundedPopup(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	const (
		wsCaption    = 0x00C00000
		wsThickFrame = 0x00040000
	)
	gwlStyle := ^uintptr(15)
	style, _, _ := pGetWindowLongW.Call(hwnd, gwlStyle)
	pSetWindowLongW.Call(hwnd, gwlStyle, style & ^uintptr(wsCaption|wsThickFrame))
	// SWP_FRAMECHANGED 让样式立即生效
	const swpNoSize, swpNoMove, swpNoZOrder, swpNoActivate, swpFrameChanged = 0x2, 0x1, 0x4, 0x10, 0x20
	pSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0,
		swpNoSize|swpNoMove|swpNoZOrder|swpNoActivate|swpFrameChanged)
	RoundCorners(hwnd)
}

// RoundCorners 强制 DWM 圆角（DWMWA_WINDOW_CORNER_PREFERENCE，Win11）。
func RoundCorners(hwnd uintptr) {
	const dwmwaWindowCornerPreference = 33
	const dwmwcpRound = 2
	v := uint32(dwmwcpRound)
	pDwmSetWindowAttribute.Call(hwnd, uintptr(dwmwaWindowCornerPreference),
		uintptr(unsafe.Pointer(&v)), unsafe.Sizeof(v))
}

// EnableDarkTitlebar 按系统深浅色染标题栏（DWMWA_USE_IMMERSIVE_DARK_MODE）。
func EnableDarkTitlebar(hwnd uintptr, dark bool) {
	const dwmwaUseImmersiveDarkMode = 20
	v := uint32(0)
	if dark {
		v = 1
	}
	pDwmSetWindowAttribute.Call(hwnd, dwmwaUseImmersiveDarkMode,
		uintptr(unsafe.Pointer(&v)), unsafe.Sizeof(v))
}

// FixWindowSize 禁止拖拽缩放与最大化（流程窗口：选通道/扫码不应被拉成全屏）。
// walk 判定固定尺寸的依据正是无 WS_THICKFRAME。
func FixWindowSize(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	const wsThickFrame, wsMaximizeBox = 0x00040000, 0x00010000
	gwlStyle := ^uintptr(15)
	style, _, _ := pGetWindowLongW.Call(hwnd, gwlStyle)
	pSetWindowLongW.Call(hwnd, gwlStyle, style & ^uintptr(wsThickFrame|wsMaximizeBox))
	const swpNoSize, swpNoMove, swpNoZOrder, swpNoActivate, swpFrameChanged = 0x2, 0x1, 0x4, 0x10, 0x20
	pSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0,
		swpNoSize|swpNoMove|swpNoZOrder|swpNoActivate|swpFrameChanged)
}
