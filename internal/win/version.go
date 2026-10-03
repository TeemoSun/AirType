//go:build windows

package win

import (
	"sync"
	"syscall"
	"unsafe"
)

// OsVersion 是 Windows 版本号。
type OsVersion struct {
	Major, Minor, Build uint32
}

var (
	osVersionOnce sync.Once
	osVersionVal  OsVersion
)

var pRtlGetVersion = syscall.NewLazyDLL("ntdll.dll").NewProc("RtlGetVersion")

// WindowsVersion 返回真实系统版本。不用 GetVersionExW：Win8.1 起它受
// manifest 兼容性声明影响，会被垫片固定返回旧版本号；RtlGetVersion 永远
// 给真实值。
func WindowsVersion() OsVersion {
	osVersionOnce.Do(func() {
		type osVersionInfoW struct {
			size         uint32
			major, minor uint32
			build        uint32
			platformId   uint32
			csdVersion   [128]uint16
			spMajor      uint16
			spMinor      uint16
			suiteMask    uint16
			productType  uint8
			reserved     uint8
		}
		var vi osVersionInfoW
		vi.size = uint32(unsafe.Sizeof(vi))
		pRtlGetVersion.Call(uintptr(unsafe.Pointer(&vi)))
		osVersionVal = OsVersion{Major: vi.major, Minor: vi.minor, Build: vi.build}
	})
	return osVersionVal
}

// IsWin11 是否 Windows 11（build ≥ 22000，含 Server 2022 之外的个人版）。
func IsWin11() bool {
	return WindowsVersion().Build >= 22000
}

// HasSystemBackdrop 是否支持系统级窗口材质 Mica/Acrylic
// （DWMWA_SYSTEMBACKDROP_TYPE，Win11 22H2 / build ≥ 22621）。
func HasSystemBackdrop() bool {
	return WindowsVersion().Build >= 22621
}
