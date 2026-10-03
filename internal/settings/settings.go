// Package settings 持久化用户偏好到数据目录下的 settings.json。
//
// 写入走"临时文件 + 原子改名"，避免断电/崩溃留下半截 JSON；
// 文件不存在或损坏时返回零值设置，程序以默认值继续运行。
package settings

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// FileName 是设置文件在数据目录中的名字。
const FileName = "settings.json"

// Settings 是全部用户偏好。新增字段时保持 JSON 小写驼峰。
type Settings struct {
	// AutoEnter：消息注入完成后自动补一次回车（把消息发送出去）。
	AutoEnter bool `json:"autoEnter"`
}

// Load 读取设置；文件不存在返回零值。文件损坏只报错给调用方记录，
// 不阻断启动。
func Load(dir string) (Settings, error) {
	var s Settings
	data, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return s, nil
		}
		return s, err
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return Settings{}, err
	}
	return s, nil
}

// Save 原子写入设置。
func Save(dir string, s Settings) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	final := filepath.Join(dir, FileName)
	tmp, err := os.CreateTemp(dir, FileName+".tmp*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // 改名成功后为 no-op
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil { // 断电安全：改名前刷盘
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, final)
}
