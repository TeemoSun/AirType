// Package qqbot 对接 QQ 开放平台机器人：扫码绑定（q.qq.com lite 绑定接口）、
// access token 管理、WebSocket 网关收信（C2C 私聊消息）。
//
// 协议参考：@tencent-connect/qqbot-connector（扫码绑定）、
// @tencent-connect/qqbot-nodejs 与 tencent-connect/botgo（token 与网关）。
package qqbot

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/TeemoSun/AirType/internal/secret"
)

// credsFile 是 QQ 机器人凭据在数据目录下的持久化文件名。
const credsFile = "qqbot.json"

// Creds 是扫码绑定成功后持久化的机器人凭据。
type Creds struct {
	AppID      string `json:"appId"`
	AppSecret  string `json:"appSecret"`
	UserOpenid string `json:"userOpenid"` // 发起绑定的用户（用于来源过滤）
}

// LoadCreds 读取已保存的凭据；未绑定时返回 ok=false。
// 文件内容经 DPAPI 加密（旧版明文读取后自动迁移）；
// 解密失败（换用户/机器）返回错误，调用方应引导重新扫码绑定。
func LoadCreds(dir string) (Creds, bool, error) {
	var c Creds
	data, err := os.ReadFile(filepath.Join(dir, credsFile))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return c, false, nil
		}
		return c, false, err
	}
	plain, sealed, err := secret.Open(data)
	if err != nil {
		return Creds{}, false, fmt.Errorf("qqbot: 凭据解密失败（可能来自其他用户/机器）: %w", err)
	}
	if err := json.Unmarshal(plain, &c); err != nil {
		return Creds{}, false, err
	}
	if c.AppID == "" || c.AppSecret == "" {
		return Creds{}, false, nil
	}
	if !sealed {
		_ = SaveCreds(dir, c) // 旧版明文：立即迁移为加密存储
	}
	return c, true, nil
}

// SaveCreds 原子写入凭据（DPAPI 加密后落盘）。
func SaveCreds(dir string, c Creds) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	cipher, err := secret.Seal(append(data, '\n'))
	if err != nil {
		return err
	}
	final := filepath.Join(dir, credsFile)
	tmp, err := os.CreateTemp(dir, credsFile+".tmp*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(cipher); err != nil {
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

// ClearCreds 删除凭据（解绑）。
func ClearCreds(dir string) error {
	err := os.Remove(filepath.Join(dir, credsFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
