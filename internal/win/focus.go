//go:build windows

package win

import (
	"fmt"
	"syscall"
	"time"
	"unsafe"
)

var (
	pGetGUIThreadInfo     = user32.NewProc("GetGUIThreadInfo")
	pSystemParametersInfo = user32.NewProc("SystemParametersInfoW")
	pGetClassNameW        = user32.NewProc("GetClassNameW")
	pIsWindow             = user32.NewProc("IsWindow")
	pGetWindowLongW       = user32.NewProc("GetWindowLongW")
	pSetWindowLongW       = user32.NewProc("SetWindowLongW")
)

// guiThreadInfo 对应 Win32 GUITHREADINFO（x64 下 80 字节）。
type guiThreadInfo struct {
	cbSize       uint32
	flags        uint32
	hwndActive   uintptr
	hwndFocus    uintptr
	hwndCapture  uintptr
	hwndMenuOwner uintptr
	hwndMoveSize uintptr
	hwndCaret    uintptr
	rcCaret      struct{ Left, Top, Right, Bottom int32 }
}

// FocusSnapshot 记录"打开弹窗前"的前台窗口与其内部焦点控件（方案 §4.2）。
type FocusSnapshot struct {
	Foreground uintptr // 顶层前台窗口
	Focus      uintptr // 前台窗口内部的焦点控件
}

// CaptureFocus 采集当前焦点快照。
func CaptureFocus() FocusSnapshot {
	fg := uintptr(Foreground())
	if fg == 0 {
		return FocusSnapshot{}
	}
	var gti guiThreadInfo
	gti.cbSize = uint32(unsafe.Sizeof(gti))
	tid, _, _ := pGetWindowThreadProcessId.Call(fg, 0)
	focus := uintptr(0)
	if tid != 0 {
		if ok, _, _ := pGetGUIThreadInfo.Call(tid, uintptr(unsafe.Pointer(&gti))); ok != 0 {
			focus = gti.hwndFocus
		}
	}
	if focus == 0 {
		focus = fg
	}
	return FocusSnapshot{Foreground: fg, Focus: focus}
}

// RestoreFocus 把快照的前台窗口带回前台（复用 Activate 的三级兜底）。
// 目标窗口已关闭时返回错误，由调用方走剪贴板兜底。
func RestoreFocus(s FocusSnapshot) error {
	if s.Foreground == 0 {
		return fmt.Errorf("win: 无效的焦点快照")
	}
	if alive, _, _ := pIsWindow.Call(s.Foreground); alive == 0 {
		return fmt.Errorf("win: 目标窗口已关闭")
	}
	return Activate(Hwnd(s.Foreground), 800*time.Millisecond)
}

// IsWindowAlive 判断窗口句柄是否仍有效。
func IsWindowAlive(h uintptr) bool {
	alive, _, _ := pIsWindow.Call(h)
	return alive != 0
}

// ForegroundClassName 返回当前前台窗口的类名（识别上下文菜单 #32768 等）。
func ForegroundClassName() string {
	buf := make([]uint16, 256)
	ret, _, _ := pGetClassNameW.Call(uintptr(Foreground()), uintptr(unsafe.Pointer(&buf[0])), 256)
	if ret == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf)
}

const spiGetWorkArea = 0x0030

// WorkArea 返回主显示器工作区（不含任务栏）尺寸，用于历史弹窗锚定。
func WorkArea() (width, height int32) {
	var rect struct{ Left, Top, Right, Bottom int32 }
	pSystemParametersInfo.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&rect)), 0)
	return rect.Right - rect.Left, rect.Bottom - rect.Top
}

const (
	wsExTopmost    = 0x00000008
	wsExToolWindow = 0x00000080
)

// MakeTopmostToolWindow 让窗口置顶且不出现在任务栏（历史弹窗形态）。
func MakeTopmostToolWindow(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	gwlExStyle := ^uintptr(19) // GWL_EXSTYLE(-20) 的补码表示
	style, _, _ := pGetWindowLongW.Call(hwnd, gwlExStyle)
	pSetWindowLongW.Call(hwnd, gwlExStyle, style|wsExTopmost|wsExToolWindow)
}
