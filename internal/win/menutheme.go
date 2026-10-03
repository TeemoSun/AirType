//go:build windows

package win

import (
	"syscall"
	"unsafe"
)

// 原生菜单（托盘右键、上下文菜单）深色化。uxtheme 以非公开"序号导出"
// 提供：135=SetPreferredAppMode(AllowDark)、136=FlushMenuThemes
// （Win10 1809 起稳定，已在 Win11 build 26300 机器码验证：135 开头是
// mov eax,[rip+d]; xchg ecx,[rip+d]; ret —— 语义正是"存新模式返旧模式"，
// 136 开头是尾调用跳转）。
//
// 两个安全措施：
//   - 序号必须用整数直查 GetProcAddress（Go 的 syscall.NewProc("#N") 是
//     按字符串名查找，永远找不到——首版踩坑）；
//   - 序号属非公开 ABI，未来构建可能漂移：调用前校验机器码指纹，不符
//     则静默放弃（菜单留在浅色，无副作用）。

var (
	kernel32DLL     = syscall.NewLazyDLL("kernel32.dll")
	pLoadLibraryW   = kernel32DLL.NewProc("LoadLibraryW")
	pGetProcAddress = kernel32DLL.NewProc("GetProcAddress")
	pReadMem        = kernel32DLL.NewProc("ReadProcessMemory")
)

// appMode 值（uxtheme 私有枚举）：AllowDark = 菜单自动跟随系统深浅色，
// 运行中切主题无需重设。
const appModeAllowDark = 1

func uxthemeProcByOrdinal(ordinal int) uintptr {
	name, _ := syscall.UTF16PtrFromString("uxtheme.dll")
	h, _, _ := pLoadLibraryW.Call(uintptr(unsafe.Pointer(name)))
	if h == 0 {
		return 0
	}
	addr, _, _ := pGetProcAddress.Call(h, uintptr(ordinal)) // 参数低字为序号 → 按序号查
	return addr
}

// memAt 读本进程内 addr 处的 n 字节（已加载模块的 .text 段可读）。
// 走 ReadProcessMemory + 当前进程伪句柄：地址全程作为整数参数，
// 避免 uintptr→unsafe.Pointer 转换（vet 禁止）。
func memAt(addr uintptr, n int) []byte {
	buf := make([]byte, n)
	var read uintptr
	pReadMem.Call(^uintptr(0), addr, // -1 = GetCurrentProcess 伪句柄
		uintptr(unsafe.Pointer(&buf[0])), uintptr(n), uintptr(unsafe.Pointer(&read)))
	return buf
}

// EnableDarkMenus 让之后弹出的所有原生 HMENU 菜单跟随系统深浅色
// （Win10 1809+；浅色系统下无感）。须在任何菜单首次弹出前调用一次。
func EnableDarkMenus() {
	setMode := uxthemeProcByOrdinal(135)
	// SetPreferredAppMode 指纹：8B 05 .. .. .. .. 87 0D .. .. .. .. C3
	pat := memAt(setMode, 13)
	if len(pat) < 13 || pat[0] != 0x8B || pat[1] != 0x05 ||
		pat[6] != 0x87 || pat[7] != 0x0D || pat[12] != 0xC3 {
		return
	}
	syscall.SyscallN(setMode, appModeAllowDark)
	// FlushMenuThemes 让已缓存的菜单主题立即刷新（仅指纹相符才调）
	if fb := memAt(uxthemeProcByOrdinal(136), 1); len(fb) == 1 && fb[0] == 0xE9 {
		syscall.SyscallN(uxthemeProcByOrdinal(136))
	}
}
