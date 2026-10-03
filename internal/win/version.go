//go:build windows

package win

import (
	"sync"
	"syscall"
	"unsafe"
)

var pRtlGetVersion = syscall.NewLazyDLL("ntdll.dll").NewProc("RtlGetVersion")

var (
	osBuildOnce sync.Once
	osBuildVal  uint32
)

// rtlOsVersionInfoW 对应 RTL_OSVERSIONINFOW（dwOSVersionInfoSize 起头）。
type rtlOsVersionInfoW struct {
	size                uint32
	major, minor, build uint32
	platform            uint32
	csdVersion          [128]uint16
}

// OsBuild 返回系统 build 号（失败返回 0）。用 ntdll 的 RtlGetVersion 而非
// GetVersionExW：后者受兼容性垫片影响会把新系统谎报成 6.2。
func OsBuild() uint32 {
	osBuildOnce.Do(func() {
		if err := pRtlGetVersion.Find(); err != nil {
			return
		}
		var info rtlOsVersionInfoW
		info.size = uint32(unsafe.Sizeof(info))
		pRtlGetVersion.Call(uintptr(unsafe.Pointer(&info)))
		osBuildVal = info.build
	})
	return osBuildVal
}

// IsWin11 判定 Win11（build ≥ 22000），供按版本选字体/开特性的调用方使用。
// 判不出按 false 处理（回退 Win10 形态，功能不受影响）。
func IsWin11() bool {
	return OsBuild() >= 22000
}
