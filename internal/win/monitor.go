//go:build windows

package win

import (
	"unsafe"
)

var (
	pMonitorFromPoint = user32.NewProc("MonitorFromPoint")
	pGetMonitorInfoW  = user32.NewProc("GetMonitorInfoW")
	pGetCursorPos     = user32.NewProc("GetCursorPos")
)

// Rect 是屏幕矩形（与 walk.Rectangle 布局一致）。
type Rect struct {
	X, Y, Width, Height int32
}

type point struct{ X, Y int32 }

type monitorInfo struct {
	cbSize    uint32
	rcMonitor struct{ Left, Top, Right, Bottom int32 }
	rcWork    struct{ Left, Top, Right, Bottom int32 }
	dwFlags   uint32
}

const monitorDefaultToNearest = 2

// WorkAreaAtCursor 返回鼠标所在显示器的工作区（不含任务栏）。
// 点击托盘图标时鼠标恰在图标上，以此实现"弹窗出现在图标所在屏"的锚定。
func WorkAreaAtCursor() Rect {
	var pt point
	if ok, _, _ := pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt))); ok == 0 {
		w, h := workArea()
		return Rect{Width: w, Height: h}
	}
	// POINT 按值传参：x64 下打包为一个 uintptr
	ptVal := uintptr(uint64(uint32(pt.X)) | uint64(uint32(pt.Y))<<32)
	hm, _, _ := pMonitorFromPoint.Call(ptVal, monitorDefaultToNearest)
	if hm == 0 {
		w, h := workArea()
		return Rect{Width: w, Height: h}
	}
	var mi monitorInfo
	mi.cbSize = uint32(unsafe.Sizeof(mi))
	if ok, _, _ := pGetMonitorInfoW.Call(hm, uintptr(unsafe.Pointer(&mi))); ok == 0 {
		w, h := workArea()
		return Rect{Width: w, Height: h}
	}
	return Rect{
		X:      mi.rcWork.Left,
		Y:      mi.rcWork.Top,
		Width:  mi.rcWork.Right - mi.rcWork.Left,
		Height: mi.rcWork.Bottom - mi.rcWork.Top,
	}
}
