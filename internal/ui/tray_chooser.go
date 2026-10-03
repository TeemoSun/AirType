//go:build windows

package ui

import (
	"time"

	. "github.com/lxn/walk/declarative"

	"github.com/lxn/walk"

	"github.com/TeemoSun/AirType/internal/win"
)

// ShowChannelChooser 弹出通道选择窗口（未绑定态，微信/QQ 对等）。线程安全。
func (t *Tray) ShowChannelChooser() {
	t.mw.Synchronize(func() {
		t.ensureChooserWindow()
		if t.chooserWin != nil {
			t.chooserWin.Show()
			_ = win.Activate(win.Hwnd(t.chooserWin.Handle()), 2*time.Second)
		}
	})
}

// ensureChooserWindow 惰性创建通道选择窗口（须在 UI 线程调用）。
// 系统深浅色切换后销毁重建，跟随主题。
func (t *Tray) ensureChooserWindow() {
	_, dark := currentPalette()
	if t.chooserWin != nil {
		if t.chooserDark == dark {
			return
		}
		t.chooserWin.Dispose() // 主题变了：重建
		t.chooserWin = nil
	}
	pal, _ := currentPalette()

	var w *walk.MainWindow
	var btnWX, btnQQ *walk.PushButton
	err := MainWindow{
		AssignTo:   &w,
		Title:      "AirType · 选择绑定通道",
		Size:       Size{Width: 400, Height: 300},
		Background: SolidColorBrush{Color: pal.Window},
		Layout:     VBox{Margins: Margins{Left: 32, Top: 30, Right: 32, Bottom: 28}, Spacing: 10},
		Children: []Widget{
			Label{
				Text:      "选择消息通道",
				Font:      Font{Family: "Segoe UI", PointSize: 16, Bold: true},
				TextColor: pal.Text,
			},
			// 说明分两行显式排版：walk Label 按单行测高，一整段长文案
			// 会被截掉第二行（首版 bug，评审实锤）。
			Label{
				Text:      "扫码绑定后，手机上发的消息会直接在电脑上打出来。",
				TextColor: pal.TextSecondary,
			},
			Label{
				Text:      "换绑通道：托盘右键 → 切换通道。",
				TextColor: pal.TextSecondary,
			},
			Composite{
				Layout: HBox{Margins: Margins{Left: 0, Top: 14, Right: 0, Bottom: 0}, Spacing: 16},
				Children: []Widget{
					PushButton{
						AssignTo:      &btnWX,
						Text:          "微信",
						MinSize:       Size{Width: 152, Height: 44},
						StretchFactor: 1,
						Font:          Font{Family: "Segoe UI", PointSize: 12},
						OnClicked: func() {
							w.Hide()
							if t.cfg.ChooseChannel != nil {
								t.cfg.ChooseChannel(ChoiceWeChat)
							}
						},
					},
					PushButton{
						AssignTo:      &btnQQ,
						Text:          "QQ 机器人",
						MinSize:       Size{Width: 152, Height: 44},
						StretchFactor: 1,
						Font:          Font{Family: "Segoe UI", PointSize: 12},
						OnClicked: func() {
							w.Hide()
							if t.cfg.ChooseChannel != nil {
								t.cfg.ChooseChannel(ChoiceQQ)
							}
						},
					},
				},
			},
			VSpacer{},
		},
	}.Create()
	if err != nil {
		t.cfg.Logger.Error("创建通道选择窗口失败", "err", err)
		return
	}
	t.chooserWin = w
	t.chooserDark = dark
	// 固定尺寸：流程窗不该被拉成全屏
	win.FixWindowSize(uintptr(w.Handle()))
	// 点 ✕ 只隐藏不销毁：walk 默认 WM_CLOSE 会 Dispose 窗口，
	// 而惰性创建指针不清空，之后 Show 静默失效（左键从此无响应）。
	w.Closing().Attach(func(canceled *bool, reason walk.CloseReason) {
		*canceled = true
		w.Hide()
	})
	win.RoundCorners(uintptr(w.Handle()))
	win.EnableDarkTitlebar(uintptr(w.Handle()), dark)
	if dark {
		// 深色窗口底上的原生按钮必须切深色主题，否则是刺眼的白色块
		if btnWX != nil {
			win.SetWindowThemeDark(uintptr(btnWX.Handle()))
		}
		if btnQQ != nil {
			win.SetWindowThemeDark(uintptr(btnQQ.Handle()))
		}
	}
}
