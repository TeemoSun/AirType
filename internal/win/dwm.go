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
)

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
