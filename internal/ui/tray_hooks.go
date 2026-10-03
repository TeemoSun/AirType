//go:build windows

package ui

import (
	"time"

	"github.com/TeemoSun/AirType/internal/win"
)

// 本文件集中放置供 cmd/traytest 冒烟工具使用的测试钩子。
// 它们的消费者是外部程序而非 _test.go，无法挪进 export_test.go。

// HidePopupForTest 收起历史弹窗（供自动化测试模拟点击 ✕）。线程安全。
func (t *Tray) HidePopupForTest() {
	t.mw.Synchronize(func() {
		if t.popup != nil && t.popup.win.Visible() {
			t.popup.hide()
		}
	})
}

// PopupIsShownForTest 返回弹窗窗口当前是否可见（供测试检测闪关）。线程安全。
func (t *Tray) PopupIsShownForTest() bool {
	done := make(chan bool, 1)
	t.mw.Synchronize(func() {
		done <- t.popup != nil && t.popup.win.Visible()
	})
	select {
	case v := <-done:
		return v
	case <-time.After(2 * time.Second):
		return false
	}
}

// PopupCopyForTest 复制弹窗第 i 条（模拟单击条目），供剪贴板回读验证。线程安全。
func (t *Tray) PopupCopyForTest(i int) {
	t.mw.Synchronize(func() {
		if t.popup == nil {
			return
		}
		id, ok := t.popup.itemIDAt(i)
		if !ok {
			return
		}
		t.popup.copyAt(id)
	})
}

// AutoEnterCheckedForTest 返回"自动回车"菜单项勾选态（供自动化测试）。线程安全。
func (t *Tray) AutoEnterCheckedForTest() bool {
	done := make(chan bool, 1)
	t.mw.Synchronize(func() {
		done <- t.autoEnter != nil && t.autoEnter.Checked()
	})
	select {
	case v := <-done:
		return v
	case <-time.After(2 * time.Second):
		return false
	}
}

// TriggerAutoEnterForTest 模拟点击"自动回车"菜单项（走完整切换回调链）。线程安全。
func (t *Tray) TriggerAutoEnterForTest() {
	t.mw.Synchronize(t.toggleAutoEnterMenu)
}

// TrayIconVisibleForTest 返回托盘图标当前可见态（供守护链路测试）。线程安全。
func (t *Tray) TrayIconVisibleForTest() bool {
	done := make(chan bool, 1)
	t.mw.Synchronize(func() {
		done <- t.ni != nil && t.ni.Visible()
	})
	select {
	case v := <-done:
		return v
	case <-time.After(2 * time.Second):
		return false
	}
}

// SimulateTaskbarRestartForTest 向托盘主窗口投递 TaskbarCreated，
// 模拟 explorer/任务栏重建（走真实子类化链路）。线程安全。
func (t *Tray) SimulateTaskbarRestartForTest() {
	t.mw.Synchronize(func() {
		win.PostTaskbarCreatedForTest(uintptr(t.mw.Handle()))
	})
}
