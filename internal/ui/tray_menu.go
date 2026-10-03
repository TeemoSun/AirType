//go:build windows

package ui

import (
	"github.com/lxn/walk"
)

// buildMenu 构建右键菜单（须在 UI 线程调用）。按功能分组：
//
//	查看历史（默认项，双击托盘同效）
//	暂停打字 / 自动回车
//	──
//	切换通道…
//	──
//	打开日志 / 清空历史
//	──
//	开机自启
//	──
//	退出
func (t *Tray) buildMenu() {
	menu := t.ni.ContextMenu()

	add := func(text string, onTrigger func()) *walk.Action {
		a := walk.NewAction()
		a.SetText(text)
		a.Triggered().Attach(onTrigger)
		menu.Actions().Add(a)
		return a
	}

	hist := add("查看历史", func() { t.ShowHistory() })
	_ = hist.SetDefault(true) // 加粗：默认动作

	pause := add("暂停打字", func() {
		if t.cfg.TogglePause == nil {
			return
		}
		t.SetPaused(t.cfg.TogglePause())
	})
	t.pauseAction = pause

	autoEnter := walk.NewAction()
	autoEnter.SetText("自动回车")
	autoEnter.SetCheckable(true)
	if t.cfg.AutoEnterEnabled != nil {
		autoEnter.SetChecked(t.cfg.AutoEnterEnabled())
	}
	autoEnter.Triggered().Attach(func() { t.toggleAutoEnterMenu() })
	menu.Actions().Add(autoEnter)
	t.autoEnter = autoEnter

	menu.Actions().Add(walk.NewSeparatorAction())

	logout := add("切换通道…", func() {
		if t.cfg.Logout != nil {
			t.cfg.Logout()
		}
	})
	t.logoutAction = logout
	t.applyChannelMode() // 初始 ChannelNone：隐藏"切换通道"

	menu.Actions().Add(walk.NewSeparatorAction())

	add("打开日志", func() {
		if t.cfg.OpenLog != nil {
			t.cfg.OpenLog()
		}
	})
	add("清空历史", func() {
		if t.cfg.History == nil {
			return
		}
		t.cfg.History.Clear()
		_ = t.ni.ShowInfo("AirType", "历史已清空")
	})

	menu.Actions().Add(walk.NewSeparatorAction())

	autostart := walk.NewAction()
	autostart.SetText("开机自启")
	autostart.SetCheckable(true)
	if t.cfg.AutostartEnabled != nil {
		autostart.SetChecked(t.cfg.AutostartEnabled())
	}
	autostart.Triggered().Attach(func() {
		if t.cfg.AutostartSet == nil {
			return
		}
		want := !autostart.Checked()
		if err := t.cfg.AutostartSet(want); err != nil {
			t.cfg.Logger.Error("设置开机自启失败", "want", want, "err", err)
			_ = t.ni.ShowError("AirType", "设置开机自启失败；详情见日志")
			return
		}
		autostart.SetChecked(want)
	})
	menu.Actions().Add(autostart)

	menu.Actions().Add(walk.NewSeparatorAction())

	add("退出", func() {
		_ = t.ni.SetVisible(false)
		_ = t.ni.Dispose()
		t.mw.Close()
	})
}

// toggleAutoEnterMenu 处理"自动回车"菜单点击：回调业务层切换，
// 再以返回值刷新勾选态（菜单项本身不自持状态，避免与持久化设置漂移）。
func (t *Tray) toggleAutoEnterMenu() {
	if t.cfg.ToggleAutoEnter == nil {
		return
	}
	_ = t.autoEnter.SetChecked(t.cfg.ToggleAutoEnter())
}
