//go:build windows

package ui

import (
	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"

	"github.com/TeemoSun/AirType/internal/win"
)

// 通道选择窗·材质呈现（方案 §10.7）：Mica 主窗语义，标题栏交给 DWM
// （透明 caption），全部内容（标题/说明/两个按钮）经 D2D 直绘。

// 选择窗布局常量（96dpi 逻辑单位）。
const (
	matChooserBtnH   = float32(40.0)
	matChooserBtnGap = float32(12.0)
	matChooserMargin = float32(32.0)
)

// ensureChooserWindowMaterial 创建 Mica 版选择窗；失败返回 false 回退实色。
func (t *Tray) ensureChooserWindowMaterial(dark bool) bool {
	pal, _ := currentPalette()
	var w *walk.MainWindow
	var host *walk.Composite
	err := MainWindow{
		AssignTo:   &w,
		Title:      "AirType · 选择绑定通道",
		Size:       Size{Width: 400, Height: 300},
		Background: SolidColorBrush{Color: pal.Window},
		Layout:     VBox{MarginsZero: true, SpacingZero: true},
		Children: []Widget{
			Composite{
				AssignTo:      &host,
				Layout:        VBox{MarginsZero: true, SpacingZero: true},
				StretchFactor: 1,
			},
		},
	}.Create()
	if err != nil {
		t.cfg.Logger.Error("创建材质选择窗失败", "err", err)
		return false
	}
	t.chooserWin = w
	t.chooserDark = dark
	win.FixWindowSize(uintptr(w.Handle()))
	// 点 ✕ 只隐藏不销毁（与实色呈现同语义）
	w.Closing().Attach(func(canceled *bool, reason walk.CloseReason) {
		*canceled = true
		w.Hide()
	})
	win.EnableDarkTitlebar(uintptr(w.Handle()), dark)
	// Mica 之下 caption 透明化（NONE），窗口整体与材质融为一体
	win.SetWindowCaptionColor(uintptr(w.Handle()), win.DwmColorNone)
	win.SetWindowNoBorder(uintptr(w.Handle()))
	enableMaterialBackdrop(uintptr(w.Handle()), false) // Mica（主窗语义）

	mat, err := newMatCanvas(host, pal, t.paintChooserMaterial)
	mat.onErr = func(err error) { t.cfg.Logger.Error("材质渲染器创建失败", "err", err) }
	if err != nil {
		t.cfg.Logger.Error("创建材质画布失败", "err", err)
		return false
	}
	t.chooserMat = mat
	mat.trackHover(func() { mat.redraw() })
	mat.w.MouseMove().Attach(func(x, y int, _ walk.MouseButton) {
		idx := t.chooserBtnAt(x, y)
		if idx != t.chooserHover {
			t.chooserHover = idx
			mat.redraw()
		}
	})
	mat.w.MouseDown().Attach(func(x, y int, button walk.MouseButton) {
		if button != walk.LeftButton {
			return
		}
		idx := t.chooserBtnAt(x, y)
		if idx >= 0 {
			t.chooserPressed = idx + 1
			mat.redraw()
		}
	})
	mat.w.MouseUp().Attach(func(x, y int, button walk.MouseButton) {
		if button != walk.LeftButton {
			return
		}
		was := t.chooserPressed
		t.chooserPressed = 0
		idx := t.chooserBtnAt(x, y)
		mat.redraw()
		// 按住拖出后松开不触发（标准按钮语义）
		if was == idx+1 && idx >= 0 {
			t.chooserHideAndChoose(idx == 0)
		}
	})
	if !t.matChooserEnsureRenderer() {
		return false
	}
	return true
}

// matChooserEnsureRenderer 惰性创建渲染器。
func (t *Tray) matChooserEnsureRenderer() bool {
	if t.chooserMat.r != nil {
		return true
	}
	pw, ph := win.ClientRectPixels(uintptr(t.chooserWin.Handle()))
	if pw == 0 || ph == 0 {
		return false
	}
	r, err := win.NewD2DRenderer(uintptr(t.chooserWin.Handle()), pw, ph)
	if err != nil {
		t.cfg.Logger.Error("创建 D2D 渲染器失败，回退实色", "err", err)
		return false
	}
	t.chooserMat.r = r
	return true
}

// chooserBtnAt 命中测试：0=微信（主按钮）1=QQ（次级）-1=无。
func (t *Tray) chooserBtnAt(x, y int) int {
	if t.chooserMat == nil || t.chooserMat.r == nil {
		return -1
	}
	w, h := t.chooserMat.r.Size()
	fx, fy := float32(x), float32(y)
	btnW := (w - matChooserMargin*2 - matChooserBtnGap) / 2
	btnY := h - 28 - matChooserBtnH
	if fy < btnY || fy > btnY+matChooserBtnH || fx < matChooserMargin {
		return -1
	}
	if fx <= matChooserMargin+btnW {
		return 0
	}
	if fx >= matChooserMargin+btnW+matChooserBtnGap && fx <= matChooserMargin+btnW*2+matChooserBtnGap {
		return 1
	}
	return -1
}

func (t *Tray) chooserHideAndChoose(weChat bool) {
	if t.chooserWin != nil {
		t.chooserWin.Hide()
	}
	if t.cfg.ChooseChannel == nil {
		return
	}
	choice := ChoiceQQ
	if weChat {
		choice = ChoiceWeChat
	}
	t.cfg.ChooseChannel(choice)
}

func (t *Tray) paintChooserMaterial(r *win.D2DRenderer, w, h float32) error {
	pal := t.chooserMat.pal
	r.BeginDraw()

	_ = r.DrawText(matChooserMargin, 30, w-64, 28, "选择消息通道",
		win.DrawTextOpts{Size: 15, Bold: true, Color: uint32(pal.Text)})
	_ = r.DrawText(matChooserMargin, 68, w-64, 18, "扫码绑定后，手机上发的消息会直接在电脑上打出来。",
		win.DrawTextOpts{Size: 9, Color: uint32(pal.TextSecondary)})
	_ = r.DrawText(matChooserMargin, 92, w-64, 18, "换绑通道：托盘右键 → 切换通道。",
		win.DrawTextOpts{Size: 9, Color: uint32(pal.TextSecondary)})

	btnW := (w - matChooserMargin*2 - matChooserBtnGap) / 2
	btnY := h - 28 - matChooserBtnH
	drawBtn := func(idx int, text string, primary bool) {
		x := matChooserMargin + float32(idx)*(btnW+matChooserBtnGap)
		fill := pal.Card
		if primary {
			switch {
			case t.chooserPressed == idx+1:
				fill = pal.AccentPressed
			case t.chooserHover == idx:
				fill = pal.AccentHover
			default:
				fill = pal.Accent
			}
		} else {
			switch {
			case t.chooserPressed == idx+1:
				fill = pal.Pressed
			case t.chooserHover == idx:
				fill = pal.Hover
			}
		}
		r.FillRoundedRect(x, btnY, btnW, matChooserBtnH, 4, uint32(fill), 1)
		if !primary {
		}
		txtColor := pal.Text
		if primary {
			txtColor = pal.OnAccent
		}
		_ = r.DrawText(x, btnY, btnW, matChooserBtnH, text,
			win.DrawTextOpts{Size: 10, Color: uint32(txtColor), VCenter: true, HCenter: true})
	}
	drawBtn(0, "微信", true)
	drawBtn(1, "QQ 机器人", false)
	return r.EndDraw()
}
