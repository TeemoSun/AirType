//go:build windows

package win

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	pRegisterWindowMessageW = user32.NewProc("RegisterWindowMessageW")
	pSetWindowLongPtrW      = user32.NewProc("SetWindowLongPtrW")
	pCallWindowProcW        = user32.NewProc("CallWindowProcW")
	pPostMessageW           = user32.NewProc("PostMessageW")
)

// RegisterWindowMessage 对同一字符串在系统范围内返回同一 id，
// 两个进程（运行中实例与被拦下的二次实例）借此对上暗号。
var (
	taskbarCreatedMsg = registerMessage("TaskbarCreated")
	wakeTrayMsg       = registerMessage("AirType.WakeTray")
)

func registerMessage(name string) uint32 {
	p, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return 0
	}
	ret, _, _ := pRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(p)))
	return uint32(ret)
}

// guardOrigProc 保存子类化前的原窗口过程；guardCb 持有回调防被 GC。
// 两者进程存活期间有效（仅一个托盘主窗口，只安装一次）。
var (
	guardOrigProc, guardCb uintptr
)

// SubclassTrayGuard 子类化 hwnd 的窗口过程，监听：
//   - TaskbarCreated（explorer/任务栏重建的系统广播）→ onTaskbarCreated
//   - AirType.WakeTray（二次实例被单实例锁拦下后的唤醒广播）→ onWake
//
// 每条消息先链回原窗口过程（walk 自己也要在 TaskbarCreated 里做重挂），
// 之后再回调宿主逻辑，保证宿主看到的是 walk 处理后的最终状态。
// 回调发生在 UI 线程（窗口过程天然如此），可直接操作窗口控件。
//
// onTrace 可为 nil；否则在链回原窗口过程之前，对每条到达的消息调用一次，
// 供宿主记录销毁类消息（WM_CLOSE/WM_DESTROY 等）的到达时刻——用于追查
// "窗口被外部销毁导致托盘僵尸"类问题（销毁发生在 GetMessage 内部派发的
// 发送型消息里时，事后无从取证）。
func SubclassTrayGuard(hwnd uintptr, onTaskbarCreated, onWake func(), onTrace func(msg uint32)) error {
	if hwnd == 0 {
		return fmt.Errorf("win: SubclassTrayGuard: hwnd 为空")
	}
	if guardOrigProc != 0 {
		return nil // 已安装
	}
	cb := syscall.NewCallback(func(h uintptr, msg uint32, wp, lp uintptr) uintptr {
		if onTrace != nil {
			onTrace(msg)
		}
		ret, _, _ := pCallWindowProcW.Call(guardOrigProc, h, uintptr(msg), wp, lp)
		switch msg {
		case taskbarCreatedMsg:
			if onTaskbarCreated != nil {
				onTaskbarCreated()
			}
		case wakeTrayMsg:
			if onWake != nil {
				onWake()
			}
		}
		return ret
	})
	prev, _, _ := pSetWindowLongPtrW.Call(hwnd, ^uintptr(3), cb) // GWL_WNDPROC = -4
	if prev == 0 {
		return fmt.Errorf("win: SetWindowLongPtrW 安装子类化失败")
	}
	guardOrigProc, guardCb = prev, cb
	return nil
}

// WakeRunningInstance 向所有顶层窗口投递唤醒消息（HWND_BROADCAST），
// 供被单实例锁拦下的第二进程在退出前调用：运行中的实例收到后重建托盘
// 图标并弹气泡。PostMessage 异步投递，不阻塞第二进程退出。
func WakeRunningInstance() {
	const hwndBroadcast = 0xFFFF
	_, _, _ = pPostMessageW.Call(hwndBroadcast, uintptr(wakeTrayMsg), 0, 0)
}

// PostTaskbarCreatedForTest 向指定窗口投递 TaskbarCreated，模拟
// explorer/任务栏重建，走真实子类化链路（供自动化测试）。
func PostTaskbarCreatedForTest(hwnd uintptr) {
	_, _, _ = pPostMessageW.Call(hwnd, uintptr(taskbarCreatedMsg), 0, 0)
}
