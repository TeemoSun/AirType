//go:build windows

package win

import (
	"fmt"
	"sync"
	"syscall"
	"unsafe"
)

var pGetWindowLongPtrW = user32.NewProc("GetWindowLongPtrW")
var pTrackMouseEvent = user32.NewProc("TrackMouseEvent")

const gwlWndProc = ^uintptr(3) // GWL_WNDPROC = -4

// subclassEntry 记录一个窗口的子类化状态；cb 必须持有，防止回调被 GC。
type subclassEntry struct {
	orig, cb uintptr
	hook     func(msg uint32, wp, lp uintptr) (handled bool, result uintptr)
}

var (
	subclassMu sync.Mutex
	subclasses = map[uintptr]*subclassEntry{}
)

// SubclassWindowProc 给窗口安装通用消息钩子（GWLP_WNDPROC 链接，与
// trayguard 的托盘守护同一手法，这里是可多窗口共用的通用形态）：
// hook 返回 handled=true 时跳过原窗口过程，否则照常链回 walk。
// 每个窗口只允许安装一次；回调在 UI 线程触发（窗口过程天然如此）。
// 返回卸载函数（重新子类化/窗口销毁前调用）。
func SubclassWindowProc(hwnd uintptr, hook func(msg uint32, wp, lp uintptr) (handled bool, result uintptr)) (func(), error) {
	if hwnd == 0 {
		return nil, fmt.Errorf("win: SubclassWindowProc: hwnd 为空")
	}
	subclassMu.Lock()
	defer subclassMu.Unlock()
	if _, exists := subclasses[hwnd]; exists {
		return nil, fmt.Errorf("win: SubclassWindowProc: hwnd 0x%X 已安装", hwnd)
	}
	e := &subclassEntry{hook: hook}
	e.cb = syscall.NewCallback(func(h uintptr, msg uint32, wp, lp uintptr) uintptr {
		if handled, res := e.hook(msg, wp, lp); handled {
			return res
		}
		ret, _, _ := pCallWindowProcW.Call(e.orig, h, uintptr(msg), wp, lp)
		return ret
	})
	prev, _, _ := pSetWindowLongPtrW.Call(hwnd, gwlWndProc, e.cb)
	if prev == 0 {
		return nil, fmt.Errorf("win: SubclassWindowProc: SetWindowLongPtrW 失败")
	}
	e.orig = prev
	subclasses[hwnd] = e
	uninstall := func() {
		subclassMu.Lock()
		defer subclassMu.Unlock()
		if subclasses[hwnd] == e {
			pSetWindowLongPtrW.Call(hwnd, gwlWndProc, e.orig)
			delete(subclasses, hwnd)
		}
	}
	return uninstall, nil
}

// trackMouseEvent 对应 TRACKMOUSEEVENT（x64 下 unsafe.Sizeof=24）。
type trackMouseEvent struct {
	cbSize      uint32
	dwFlags     uint32
	hwndTrack   uintptr
	dwHoverTime uint32
}

// TrackMouseLeave 请求系统在鼠标离开 hwnd 时投递一次 WM_MOUSELEAVE
// （TME_LEAVE）。walk 的 CustomWidget 不发 MouseLeave 事件，自绘控件的
// 悬停态靠它在进入时预约、窗口过程里清除。
func TrackMouseLeave(hwnd uintptr) {
	const tmeLeave = 0x2
	tme := trackMouseEvent{cbSize: uint32(unsafe.Sizeof(trackMouseEvent{})), dwFlags: tmeLeave, hwndTrack: hwnd}
	pTrackMouseEvent.Call(uintptr(unsafe.Pointer(&tme)))
}
