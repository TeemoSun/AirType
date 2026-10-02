//go:build windows

// Package autostart 通过计划任务实现开机自启。
//
// requireAdministrator 的 EXE 无法走 HKCU Run 键（UAE 限制），
// 注册"登录时、以最高权限运行"的计划任务一并解决自启与 UAC 弹窗
// （见开发方案 §4.5）。本程序以管理员运行，schtasks 可直接成功。
package autostart

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

const taskName = "AirType_Autostart"

func run(args ...string) (string, error) {
	cmd := exec.Command("schtasks", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

// Enabled 查询自启任务是否存在。
func Enabled() bool {
	_, err := run("/Query", "/TN", taskName)
	return err == nil
}

// Set 启用或禁用开机自启。
func Set(enable bool) error {
	if enable {
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("autostart: 获取自身路径失败: %w", err)
		}
		out, err := run("/Create", "/F", "/TN", taskName,
			"/TR", `"`+exe+`"`, "/SC", "ONLOGON", "/RL", "HIGHEST")
		if err != nil {
			return fmt.Errorf("autostart: 创建计划任务失败: %s (%w)", strings.TrimSpace(out), err)
		}
		return nil
	}
	out, err := run("/Delete", "/F", "/TN", taskName)
	if err != nil {
		// 任务本就不存在视为成功
		if !Enabled() {
			return nil
		}
		return fmt.Errorf("autostart: 删除计划任务失败: %s (%w)", strings.TrimSpace(out), err)
	}
	return nil
}
