//go:build windows

package win

import (
	"fmt"
	"syscall"
	"unsafe"
)

// 唤醒与看门狗原语。
//
// 背景（2026-10 僵尸实例事故）：walk 的 mainLoop 以 `for fb.hWnd != 0` 为
// 退出条件，但该条件只在取到一条投递型消息后才会复查。若窗口被销毁发生在
// GetMessage 内部派发的发送型（send）消息里（外部/跨线程 SendMessage 触发
// WM_CLOSE → walk Dispose → DestroyWindow），投递队列随后为空，GetMessage
// 永久阻塞——进程活着、托盘没了、单实例互斥量也释放不掉。
//
// 这里的两件武器：
//   - PingWindow/PostQuitToThread：看门狗周期性向主窗口投递 WM_NULL，强制
//     消息循环醒来复查退出条件；发现窗口已销毁则向 UI 线程投递 WM_QUIT，
//     让应用干净退出（释放互斥量），而不是僵尸常驻。
//   - 命名事件唤醒：二次实例的唤醒广播（HWND_BROADCAST）依赖窗口存在，且
//     低完整性进程向提权实例发送会被 UIPI 静默拦截；命名事件是内核对象，
//     两个限制都没有。

const (
	wmQuit            = 0x0012
	wmNull            = 0x0000
	eventModifyState  = 0x0002 // EVENT_MODIFY_STATE
	infinite          = 0xFFFFFFFF
	invalidHandleFlag = ^uintptr(0) // WAIT_FAILED
)

var (
	pPostThreadMessageW  = user32.NewProc("PostThreadMessageW")
	pCreateEventW        = kernel32.NewProc("CreateEventW")
	pOpenEventW          = kernel32.NewProc("OpenEventW")
	pSetEvent            = kernel32.NewProc("SetEvent")
	pCloseHandle         = kernel32.NewProc("CloseHandle")
	pWaitForSingleObject = kernel32.NewProc("WaitForSingleObject")
)

// WakeEventName 二次实例唤醒事件名。Local\（会话）命名空间，与单实例
// 互斥量一致；同会话内跨完整性级别可用。
const WakeEventName = `Local\AirType.Wake`

// ListenWake 创建命名事件（自动重置）并常驻等待，每次被触发向返回的
// 通道发一次信号。goroutine 随进程存续，无需显式停止。
func ListenWake() (<-chan struct{}, error) {
	p, err := syscall.UTF16PtrFromString(WakeEventName)
	if err != nil {
		return nil, err
	}
	h, _, callErr := pCreateEventW.Call(0, 0, 0, uintptr(unsafe.Pointer(p)))
	if h == 0 {
		return nil, fmt.Errorf("win: CreateEvent 失败: %v", callErr)
	}
	ch := make(chan struct{}, 1)
	go func() {
		for {
			r, _, _ := pWaitForSingleObject.Call(h, infinite)
			if r == invalidHandleFlag {
				return
			}
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	}()
	return ch, nil
}

// SignalWake 触发运行实例的唤醒事件。事件不存在（实例为不含本机制的旧
// 版本）时返回 false，调用方应回退到窗口广播。
func SignalWake() bool {
	p, err := syscall.UTF16PtrFromString(WakeEventName)
	if err != nil {
		return false
	}
	h, _, _ := pOpenEventW.Call(eventModifyState, 0, uintptr(unsafe.Pointer(p)))
	if h == 0 {
		return false
	}
	defer pCloseHandle.Call(h)
	r, _, _ := pSetEvent.Call(h)
	return r != 0
}

// CurrentThreadID 返回调用线程的 ID。
func CurrentThreadID() uint32 {
	r, _, _ := pGetCurrentThreadId.Call()
	return uint32(r)
}

// IsWindow 判断窗口句柄当前是否仍有效。
func IsWindow(hwnd uintptr) bool {
	r, _, _ := pIsWindow.Call(hwnd)
	return r != 0
}

// PingWindow 向窗口投递一条 WM_NULL：唤醒可能阻塞中的 GetMessage，让
// 消息循环复查退出条件。窗口已销毁时投递失败，返回 false。
func PingWindow(hwnd uintptr) bool {
	r, _, _ := pPostMessageW.Call(hwnd, wmNull, 0, 0)
	return r != 0
}

// PostQuitToThread 向指定线程投递 WM_QUIT（线程消息），唤醒其阻塞中的
// GetMessage 并使消息循环以退出码 0 返回。
func PostQuitToThread(threadID uint32) bool {
	r, _, _ := pPostThreadMessageW.Call(uintptr(threadID), wmQuit, 0, 0)
	return r != 0
}
