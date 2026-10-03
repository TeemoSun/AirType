//go:build windows

package win

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"
)

var (
	procCreateMutexW = kernel32.NewProc("CreateMutexW")
)

// ErrAlreadyRunning 表示已有同名单实例在运行。
var ErrAlreadyRunning = errors.New("win: 已有 AirType 实例在运行")

// guard 保存互斥量句柄，进程存活期间持有（不释放）。
var guard uintptr

// AcquireSingleInstance 以命名互斥量保证单实例；重复启动返回 ErrAlreadyRunning。
// 用 Local\（会话）命名空间：快速用户切换/多用户机器上各会话互不阻塞。
func AcquireSingleInstance(name string) error {
	p, err := syscall.UTF16PtrFromString("Local\\" + name)
	if err != nil {
		return err
	}
	h, _, callErr := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(p)))
	const errAlreadyExists = 183
	if callErr != nil && errors.Is(callErr, syscall.Errno(errAlreadyExists)) {
		return ErrAlreadyRunning
	}
	if h == 0 {
		return fmt.Errorf("win: CreateMutex 失败: %v", callErr)
	}
	guard = h
	return nil
}
