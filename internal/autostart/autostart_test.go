//go:build windows

package autostart

import (
	"os"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// TestSetRoundTrip 走真实注册表验证启用/禁用往返：启用后 Run 键存在且
// 值为带引号的当前可执行路径，禁用后值消失（本包即 HKCU 实现，无桩可替）。
func TestSetRoundTrip(t *testing.T) {
	if Enabled() {
		t.Skip("本机已启用自启，跳过以免覆盖用户设置")
	}

	if err := Set(true); err != nil {
		t.Fatalf("Set(true): %v", err)
	}
	if !Enabled() {
		t.Fatal("Set(true) 后 Enabled() 应为 true")
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		t.Fatalf("打开 Run 键: %v", err)
	}
	defer k.Close()
	v, _, err := k.GetStringValue(runName)
	if err != nil {
		t.Fatalf("读取自启值: %v", err)
	}
	exe, _ := os.Executable()
	if v != `"`+exe+`"` {
		t.Fatalf("自启值应为带引号的自身路径，得到 %q", v)
	}

	if err := Set(false); err != nil {
		t.Fatalf("Set(false): %v", err)
	}
	if Enabled() {
		t.Fatal("Set(false) 后 Enabled() 应为 false")
	}
}
