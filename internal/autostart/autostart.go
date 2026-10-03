//go:build windows

// Package autostart 通过 HKCU Run 注册表键实现开机自启。
//
// 旧实现走计划任务（schtasks /SC ONLOGON /RL HIGHEST），那是
// requireAdministrator 清单年代的设计（见开发方案 §4.5）：创建登录触发的
// 计划任务需要管理员权限，而程序现为 asInvoker 普通权限运行——实测普通
// 权限下 schtasks 一律"拒绝访问"（带不带 /RL HIGHEST 都一样），托盘里的
// 开机自启开关形同虚设。HKCU Run 无需提权、当前用户登录即生效，与产品
// "免管理员"形态一致（2026-10-03 修复，见 §10.10）。
package autostart

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows/registry"
)

const (
	runKey  = `Software\Microsoft\Windows\CurrentVersion\Run`
	runName = "AirType"
)

// Enabled 查询自启项是否存在。
func Enabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(runName)
	return err == nil
}

// Set 启用或禁用开机自启。
func Set(enable bool) error {
	if !enable {
		k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
		if err != nil {
			// 键都不存在＝本来就没启用（精简用户配置档可能无此键）
			if err == registry.ErrNotExist {
				return nil
			}
			return fmt.Errorf("autostart: 打开 Run 键失败: %w", err)
		}
		defer k.Close()
		if err := k.DeleteValue(runName); err != nil && err != registry.ErrNotExist {
			return fmt.Errorf("autostart: 删除自启项失败: %w", err)
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("autostart: 获取自身路径失败: %w", err)
	}
	// CreateKey＝打开或创建：精简配置档（如 CI runner）可能没有预置 Run 键
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("autostart: 创建 Run 键失败: %w", err)
	}
	defer k.Close()
	// 路径含空格时必须带引号，否则 Run 键解析会截断
	if err := k.SetStringValue(runName, `"`+exe+`"`); err != nil {
		return fmt.Errorf("autostart: 写入自启项失败: %w", err)
	}
	return nil
}
