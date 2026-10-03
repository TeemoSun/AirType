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
	var hostWX, hostQQ *walk.Composite
	err := MainWindow{
		AssignTo:   &w,
		Title:      "AirType · 选择绑定通道",
		Size:       Size{Width: 430, Height: 330},
		Background: SolidColorBrush{Color: pal.Window},
		Layout:     VBox{Margins: Margins{Left: 24, Top: 26, Right: 24, Bottom: 24}, Spacing: 8},
		OnKeyDown: func(key walk.Key) {
			if key == walk.KeyEscape {
				w.Hide()
			}
		},
		Children: []Widget{
			Label{
				Text:      "选择消息通道",
				Font:      Font{Family: DisplayFontFamily(), PointSize: 16, Bold: true},
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
			// 双通道卡片行：host 给出固定卡片尺寸，卡片控件铺满 host。
			Composite{
				Layout: HBox{Margins: Margins{Left: 0, Top: 12, Right: 0, Bottom: 0}, Spacing: 14},
				Children: []Widget{
					Composite{
						AssignTo:      &hostWX,
						MinSize:       Size{Width: 165, Height: 132},
						StretchFactor: 1,
						Layout:        VBox{MarginsZero: true, SpacingZero: true},
					},
					Composite{
						AssignTo:      &hostQQ,
						MinSize:       Size{Width: 165, Height: 132},
						StretchFactor: 1,
						Layout:        VBox{MarginsZero: true, SpacingZero: true},
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
	choose := func(choice ChannelChoice) func() {
		return func() {
			w.Hide()
			if t.cfg.ChooseChannel != nil {
				t.cfg.ChooseChannel(choice)
			}
		}
	}
	if _, err := newChannelCard(hostWX, "微信",
		[]string{"手机微信扫码授权", "无需加好友"}, choose(ChoiceWeChat)); err != nil {
		t.cfg.Logger.Error("创建微信卡片失败", "err", err)
		return
	}
	if _, err := newChannelCard(hostQQ, "QQ 机器人",
		[]string{"官方机器人平台", "私聊即可打字"}, choose(ChoiceQQ)); err != nil {
		t.cfg.Logger.Error("创建 QQ 卡片失败", "err", err)
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
	// 标题栏/边框与窗口底融合，消除老对话框的割裂感（Win11；旧系统静默失败）
	win.SetCaptionColor(uintptr(w.Handle()), uint32(pal.Window))
	win.SetBorderColor(uintptr(w.Handle()), uint32(pal.Stroke))
}
