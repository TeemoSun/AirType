//go:build windows

// Package secret 用 Windows DPAPI（CryptProtectData）加密本地敏感凭据。
// 密钥由操作系统绑定当前用户：同一台机器同一用户可解，
// 拷走的文件/其他账户/离线攻击者都拿不到明文。
package secret

import (
	"bytes"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// sealPrefix 是密文文件头，用于识别旧版明文文件并迁移。
var sealPrefix = []byte("DPAPIv1:")

// Seal 用 DPAPI 加密 plain，返回带前缀的密文。
func Seal(plain []byte) ([]byte, error) {
	if len(plain) == 0 {
		return nil, fmt.Errorf("secret: 空数据无需加密")
	}
	in := dataBlob(plain)
	var out windows.DataBlob
	if err := windows.CryptProtectData(&in, nil, nil, 0, nil, 0, &out); err != nil {
		return nil, fmt.Errorf("secret: DPAPI 加密失败: %w", err)
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))

	cipher := make([]byte, 0, len(sealPrefix)+int(out.Size))
	cipher = append(cipher, sealPrefix...)
	cipher = append(cipher, unsafe.Slice(out.Data, out.Size)...)
	return cipher, nil
}

// Open 解密 Seal 的产物。无前缀的输入视为旧版明文：原样返回且 sealed=false，
// 调用方据此在下次保存时迁移为密文。
func Open(data []byte) (plain []byte, sealed bool, err error) {
	if len(data) == 0 {
		return nil, false, nil
	}
	if !bytes.HasPrefix(data, sealPrefix) {
		return data, false, nil
	}
	in := dataBlob(data[len(sealPrefix):])
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, 0, &out); err != nil {
		return nil, true, fmt.Errorf("secret: DPAPI 解密失败（凭据可能来自其他用户/机器）: %w", err)
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))

	plain = make([]byte, out.Size)
	copy(plain, unsafe.Slice(out.Data, out.Size))
	return plain, true, nil
}

func dataBlob(b []byte) windows.DataBlob {
	return windows.DataBlob{Size: uint32(len(b)), Data: &b[0]}
}
