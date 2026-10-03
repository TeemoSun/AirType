//go:build windows

package win

import (
	"fmt"
	"sync"
	"syscall"
	"unsafe"
)

var pTrackMouseEvent = user32.NewProc("TrackMouseEvent")

// 通用窗口子类化：trayguard 手法推广到普通窗口（自绘控件的悬停离开、
// 无边框窗口的整面拖动都靠它）。每个 hwnd 只装一层（AirType 没有叠两
// 层的场景；trayguard 对主窗口的安装走自己的独立链路，互不相关）。
// 消息先经过 fn，fn 声明不处理的消息再链回原窗口过程（walk）。

type subclassEntry struct {
	orig, cb uintptr
	fn       func(msg uint32, wParam, lParam uintptr) (handled bool, result uintptr)
}

var (
	subclassMu sync.Mutex
	subclasses = map[uintptr]*subclassEntry{}
)

// SubclassWindow 子类化 hwnd 的窗口过程。fn 在 UI 线程被调用（窗口过程
// 天然如此）；返回 handled=true 时直接以 result 作为窗口过程返回值，
// 跳过 walk 与系统默认处理。对同一 hwnd 重复安装只是替换 fn。
func SubclassWindow(hwnd uintptr, fn func(msg uint32, wParam, lParam uintptr) (handled bool, result uintptr)) error {
	if hwnd == 0 || fn == nil {
		return fmt.Errorf("win: SubclassWindow: 参数为空")
	}
	subclassMu.Lock()
	defer subclassMu.Unlock()
	if e, ok := subclasses[hwnd]; ok {
		e.fn = fn
		return nil
	}
	e := &subclassEntry{fn: fn}
	e.cb = syscall.NewCallback(func(h uintptr, msg uint32, wp, lp uintptr) uintptr {
		e2 := func() *subclassEntry {
			subclassMu.Lock()
			defer subclassMu.Unlock()
			return subclasses[h]
		}()
		if e2 != nil {
			if handled, result := e2.fn(msg, wp, lp); handled {
				return result
			}
			return callWindowProc(e2.orig, h, msg, wp, lp)
		}
		return callWindowProc(0, h, msg, wp, lp)
	})
	prev, _, _ := pSetWindowLongPtrW.Call(hwnd, ^uintptr(3), e.cb) // GWL_WNDPROC = -4
	if prev == 0 {
		return fmt.Errorf("win: SetWindowLongPtrW 安装子类化失败")
	}
	e.orig = prev
	subclasses[hwnd] = e
	return nil
}

func callWindowProc(orig, h uintptr, msg uint32, wp, lp uintptr) uintptr {
	ret, _, _ := pCallWindowProcW.Call(orig, h, uintptr(msg), wp, lp)
	return ret
}

// TrackMouseLeave 请求系统在鼠标离开 hwnd 时投递一条 WM_MOUSELEAVE。
// 每次鼠标进入窗口只需调一次；收到消息后要再调才能继续跟踪下一次。
func TrackMouseLeave(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	type trackMouseEvent struct {
		cbSize, dwFlags uint32
		hwndTrack       uintptr
		dwHoverTime     uint32
	}
	tme := trackMouseEvent{
		cbSize:    uint32(unsafe.Sizeof(trackMouseEvent{})),
		dwFlags:   2, // TME_LEAVE
		hwndTrack: hwnd,
	}
	pTrackMouseEvent.Call(uintptr(unsafe.Pointer(&tme)))
}
