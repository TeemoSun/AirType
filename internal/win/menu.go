//go:build windows

package win

import (
	"syscall"
	"unsafe"
)

// 原生弹出菜单（历史弹窗右键：复制/删除/重新打字）。
var (
	pCreatePopupMenu  = user32.NewProc("CreatePopupMenu")
	pAppendMenuW      = user32.NewProc("AppendMenuW")
	pTrackPopupMenuEx = user32.NewProc("TrackPopupMenuEx")
	pDestroyMenu      = user32.NewProc("DestroyMenu")
)

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
