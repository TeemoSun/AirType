//go:build !windows

package secret

// 非 Windows 开发环境：DPAPI 不可用，直通存储（仅本地开发用，
// 正式发布只面向 Windows）。

// Seal 直通返回（未加密）。
func Seal(plain []byte) ([]byte, error) {
	return plain, nil
}

// Open 直通返回，sealed=false。
func Open(data []byte) (plain []byte, sealed bool, err error) {
	return data, false, nil
}
