//go:build windows

// Package win 封装本项目用到的少量 Win32 窗口能力：
// 窗口查找、前台切换（带等待与兜底）。焦点记忆（GetGUIThreadInfo）
// 在历史弹窗阶段（M4）扩展到本包。
package win

import (
	"fmt"
	"syscall"
	"time"
	"unsafe"
)

const (
	swRestore = 9
)

var (
	user32                    = syscall.NewLazyDLL("user32.dll")
	kernel32                  = syscall.NewLazyDLL("kernel32.dll")
	pFindWindowW              = user32.NewProc("FindWindowW")
	pGetForegroundWindow      = user32.NewProc("GetForegroundWindow")
	pSetForegroundWindow      = user32.NewProc("SetForegroundWindow")
	pShowWindow               = user32.NewProc("ShowWindow")
	pIsIconic                 = user32.NewProc("IsIconic")
	pGetWindowThreadProcessId = user32.NewProc("GetWindowThreadProcessId")
	pAttachThreadInput        = user32.NewProc("AttachThreadInput")
	pGetCurrentThreadId       = kernel32.NewProc("GetCurrentThreadId")
)

// Hwnd 是窗口句柄。
type Hwnd uintptr

// ByTitle 按精确窗口标题查找顶层窗口，找不到返回 0。
func ByTitle(title string) Hwnd {
	p, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return 0
	}
	h, _, _ := pFindWindowW.Call(0, uintptr(unsafe.Pointer(p)))
	return Hwnd(h)
}

// Foreground 返回当前前台窗口句柄。
func Foreground() Hwnd {
	h, _, _ := pGetForegroundWindow.Call()
	return Hwnd(h)
}

// Activate 把窗口带到前台并等待切换完成。
//
// SetForegroundWindow 受系统前台锁定限制（后台进程不能抢前台），
// 这里依次尝试：直接切换 → 模拟一次 ALT 按键解锁 → AttachThreadInput。
// 全部失败返回错误，由调用方决定是否走剪贴板兜底。
func Activate(hwnd Hwnd, timeout time.Duration) error {
	if hwnd == 0 {
		return fmt.Errorf("win: 无效窗口句柄")
	}
	if iconic, _, _ := pIsIconic.Call(uintptr(hwnd)); iconic != 0 {
		pShowWindow.Call(uintptr(hwnd), swRestore)
	}
	if Foreground() == hwnd {
		return nil
	}

	trySet := func() bool {
		pSetForegroundWindow.Call(uintptr(hwnd))
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			if Foreground() == hwnd {
				return true
			}
			time.Sleep(15 * time.Millisecond)
		}
		return false
	}

	if trySet() {
		return nil
	}

	// ALT 技巧：一次无害的 ALT 按下/释放让本进程获得前台激活权
	altKey(0)
	altKey(keyEventFKeyUp)
	if trySet() {
		return nil
	}

	// AttachThreadInput：把当前线程挂到目标窗口的输入队列后再切换
	tid, _, _ := pGetWindowThreadProcessId.Call(uintptr(hwnd), 0)
	cur, _, _ := pGetCurrentThreadId.Call()
	if tid != 0 && tid != cur {
		pAttachThreadInput.Call(cur, tid, 1)
		defer pAttachThreadInput.Call(cur, tid, 0)
		if trySet() {
			return nil
		}
	}

	return fmt.Errorf("win: 无法将窗口 0x%X 切到前台（前台锁定）", uintptr(hwnd))
}

// altKey 发送一次 VK_MENU 事件（仅用于解除前台锁定，不产生字符）。
func altKey(extraFlags uint32) {
	type ki struct {
		wVk         uint16
		wScan       uint16
		dwFlags     uint32
		time        uint32
		dwExtraInfo uintptr
	}
	type inp struct {
		typ uint32
		_   uint32
		ki  ki
		_   [8]byte
	}
	ev := inp{typ: 1, ki: ki{wVk: 0x12, dwFlags: extraFlags}}
	syscall.NewLazyDLL("user32.dll").NewProc("SendInput").Call(1, uintptr(unsafe.Pointer(&ev)), unsafe.Sizeof(ev))
}

const keyEventFKeyUp = 0x0002
