// Package paths 集中管理 AirType 的数据目录。
package paths

import (
	"fmt"
	"os"
	"path/filepath"
)

// DataDir 返回 %LOCALAPPDATA%\AirType 并确保存在。
// 注意不能用 os.UserConfigDir()——它在 Windows 返回 %AppData%（Roaming），
// 拿不到 LOCALAPPDATA（详见开发方案 §3.2）。
func DataDir() (string, error) {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		return "", fmt.Errorf("paths: 环境变量 LOCALAPPDATA 为空")
	}
	dir := filepath.Join(base, "AirType")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("paths: 创建数据目录失败: %w", err)
	}
	return dir, nil
}
