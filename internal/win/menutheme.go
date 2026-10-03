//go:build windows

package win

import (
	"sync"
	"syscall"

	"golang.org/x/sys/windows"
)

// 原生菜单深浅色跟随：uxtheme 未公开导出 SetPreferredAppMode(#135) 与
// FlushMenuThemes(#136)，这是 Win10 1809+ 唯一的非自绘菜单深色化途径。
// 必须按序号 GetProcAddress（Go 的 NewProc("#N") 是按名字找，不是按序号）。
// 调用前先做机器码指纹校验：系统更新若改动这两个桩（或未来版本移除），
// 指纹不符即静默放弃，菜单保持系统默认外观，绝不影响功能。
//
// 指纹依据（build 26300 实测），135 号开头是位置无关的"交换全局模式值"：
//
//	8B 05 xx xx xx xx   mov eax, [g_appMode]
//	87 0D xx xx xx xx   xchg [g_appMode], ecx
//	C3                  ret
var (
	uxthemeOnce        sync.Once
	uxthemeSetAppMode  uintptr
	uxthemeFlushThemes uintptr
)

func uxthemeDarkStubs() (set, flush uintptr) {
	uxthemeOnce.Do(func() {
		h, err := windows.LoadLibrary("uxtheme.dll")
		if err != nil {
			return
		}
		p135, err := windows.GetProcAddressByOrdinal(h, 135)
		if err != nil || !menuStubFingerprint(p135) {
			return
		}
		p136, err := windows.GetProcAddressByOrdinal(h, 136)
		if err != nil {
			return
		}
		uxthemeSetAppMode, uxthemeFlushThemes = p135, p136
	})
	return uxthemeSetAppMode, uxthemeFlushThemes
}

// menuStubFingerprint 校验 135 号桩开头的机器码指纹（xx 为任意重定位字节）。
// 读内存走 ReadProcessMemory（自身进程），避免 uintptr→unsafe.Pointer 的
// vet 禁用转换。
func menuStubFingerprint(proc uintptr) bool {
	var code [13]byte
	var n uintptr
	if err := windows.ReadProcessMemory(windows.CurrentProcess(), proc,
		&code[0], uintptr(len(code)), &n); err != nil || n != uintptr(len(code)) {
		return false
	}
	return code[0] == 0x8B && code[1] == 0x05 &&
		code[6] == 0x87 && code[7] == 0x0D &&
		code[12] == 0xC3
}

// preferredAppMode 值（uxtheme 内部枚举）：Default=0，AllowDark=1。
const preferredAppModeAllowDark = 1

// EnableDarkMenus 让原生弹出菜单跟随系统深浅色（托盘菜单/历史弹窗右键
// 菜单）。AllowDark 语义是"系统深色则菜单深色"，浅色系统下无副作用，
// 因此进程启动时无条件调用一次即可。运行中切换系统主题不刷新（重启后
// 跟随），属已知限制；uxtheme 桩指纹不符时静默放弃（保持系统默认外观）。
func EnableDarkMenus() {
	set, flush := uxthemeDarkStubs()
	if set == 0 || flush == 0 {
		return
	}
	syscall.SyscallN(set, preferredAppModeAllowDark)
	syscall.SyscallN(flush)
}
