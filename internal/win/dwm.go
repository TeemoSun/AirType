//go:build windows

package win

import (
	"syscall"
	"unsafe"
)

var (
	dwmapi                = syscall.NewLazyDLL("dwmapi.dll")
	pDwmSetWindowAttribute = dwmapi.NewProc("DwmSetWindowAttribute")
	pSetWindowPos         = user32.NewProc("SetWindowPos")
	pCreatePopupMenu      = user32.NewProc("CreatePopupMenu")
	pAppendMenuW          = user32.NewProc("AppendMenuW")
	pTrackPopupMenuEx     = user32.NewProc("TrackPopupMenuEx")
	pDestroyMenu          = user32.NewProc("DestroyMenu")
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

// ShowContextMenu 在鼠标位置弹出原生上下文菜单，返回选中项索引（0 起），
// 取消返回 -1。owner 为接收菜单消息的窗口句柄（须为前台窗口）。
func ShowContextMenu(owner uintptr, items []string) int {
	hmenu, _, _ := pCreatePopupMenu.Call()
	if hmenu == 0 {
		return -1
	}
	defer pDestroyMenu.Call(hmenu)
	for i, s := range items {
		p, err := syscall.UTF16PtrFromString(s)
		if err != nil {
			return -1
		}
		pAppendMenuW.Call(hmenu, 0, uintptr(1000+i), uintptr(unsafe.Pointer(p)))
	}
	var pt point
	if ok, _, _ := pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt))); ok == 0 {
		return -1
	}
	const tpmReturnCmd, tpmRightButton = 0x0100, 0x0002
	cmd, _, _ := pTrackPopupMenuEx.Call(hmenu, uintptr(tpmReturnCmd|tpmRightButton),
		uintptr(pt.X), uintptr(pt.Y), owner, 0)
	if cmd < 1000 {
		return -1
	}
	return int(cmd - 1000)
}
