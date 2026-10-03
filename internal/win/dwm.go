//go:build windows

package win

import (
	"syscall"
	"unsafe"
)

var (
	dwmapi                 = syscall.NewLazyDLL("dwmapi.dll")
	pDwmSetWindowAttribute = dwmapi.NewProc("DwmSetWindowAttribute")
	pSetWindowPos          = user32.NewProc("SetWindowPos")
	pSetWindowTheme        = syscall.NewLazyDLL("uxtheme.dll").NewProc("SetWindowTheme")
)

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

// SetCaptionColor 把标题栏染成指定色（DWMWA_CAPTION_COLOR，Win11 22000+），
// 与窗口底融合，消除"老对话框"式的标题栏割裂感。colorref 为 GDI 式
// 0x00BBGGRR（walk.Color 低 24 位即此格式）。失败静默（旧系统无此属性）。
func SetCaptionColor(hwnd uintptr, colorref uint32) {
	const dwmwaCaptionColor = 35
	pDwmSetWindowAttribute.Call(hwnd, dwmwaCaptionColor,
		uintptr(unsafe.Pointer(&colorref)), unsafe.Sizeof(colorref))
}

// SetBorderColor 染窗口边框（DWMWA_BORDER_COLOR，Win11 22000+）。
// colorref 格式同 SetCaptionColor。失败静默。
func SetBorderColor(hwnd uintptr, colorref uint32) {
	const dwmwaBorderColor = 34
	pDwmSetWindowAttribute.Call(hwnd, dwmwaBorderColor,
		uintptr(unsafe.Pointer(&colorref)), unsafe.Sizeof(colorref))
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
