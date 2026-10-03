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
// 材质可用时走 Mica 主窗呈现（全部内容 D2D 直绘）；否则回退实色呈现
// （Win11 风格自绘按钮 + 标题栏融合）。
func (t *Tray) ensureChooserWindow() {
	_, dark := currentPalette()
	if t.chooserWin != nil {
		if t.chooserDark == dark {
			return
		}
		t.chooserWin.Dispose() // 主题变了：重建
		t.chooserWin = nil
		t.chooserMat = nil
	}
	if materialOK() && t.ensureChooserWindowMaterial(dark) {
		return
	}
	pal, _ := currentPalette()

	var w *walk.MainWindow
	var hostWX, hostQQ *walk.Composite
	err := MainWindow{
		AssignTo:   &w,
		Title:      "AirType · 选择绑定通道",
		Size:       Size{Width: 400, Height: 300},
		Background: SolidColorBrush{Color: pal.Window},
		Layout:     VBox{Margins: Margins{Left: 32, Top: 30, Right: 32, Bottom: 28}, Spacing: 10},
		Children: []Widget{
			Label{
				Text:      "选择消息通道",
				Font:      Font{Family: DisplayFontFamily(), PointSize: 15, Bold: true},
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
				Layout: HBox{Margins: Margins{Left: 0, Top: 14, Right: 0, Bottom: 0}, Spacing: 12},
				// MaxSize 钳住行高：按钮是自绘控件（带 GrowableVert 标志），
				// 不钳会被拉伸抢走 VSpacer 的富余高度（实测撑到 2.5 倍）
				MaxSize: Size{Height: 40},
				Children: []Widget{
					// 按钮宿主：固定高度、横向等分，控件在 Create 后挂进来
					Composite{
						AssignTo:      &hostWX,
						Layout:        HBox{MarginsZero: true, SpacingZero: true},
						MinSize:       Size{Height: 40},
						MaxSize:       Size{Height: 40},
						StretchFactor: 1,
					},
					Composite{
						AssignTo:      &hostQQ,
						Layout:        HBox{MarginsZero: true, SpacingZero: true},
						MinSize:       Size{Height: 40},
						MaxSize:       Size{Height: 40},
						StretchFactor: 1,
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
	// 微信 = 主按钮（强调色实底），QQ = 次级按钮（卡片底+描边）
	if _, err := newFluentButton(hostWX, "微信", true, func() {
		w.Hide()
		if t.cfg.ChooseChannel != nil {
			t.cfg.ChooseChannel(ChoiceWeChat)
		}
	}); err != nil {
		t.cfg.Logger.Error("创建通道按钮失败", "err", err)
	}
	if _, err := newFluentButton(hostQQ, "QQ 机器人", false, func() {
		w.Hide()
		if t.cfg.ChooseChannel != nil {
			t.cfg.ChooseChannel(ChoiceQQ)
		}
	}); err != nil {
		t.cfg.Logger.Error("创建通道按钮失败", "err", err)
	}
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
	// 标题栏与窗口底同色、去描边：老对话框感的最大来源就地消除
	win.SetWindowCaptionColor(uintptr(w.Handle()), uint32(pal.Window))
	win.SetWindowNoBorder(uintptr(w.Handle()))
}
