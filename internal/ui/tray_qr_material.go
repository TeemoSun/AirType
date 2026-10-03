//go:build windows

package ui

import (
	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
	"github.com/skip2/go-qrcode"

	"github.com/TeemoSun/AirType/internal/win"
)

// 扫码窗·材质呈现（方案 §10.7）：Acrylic 瞬态窗，标题/提示/✕/二维码全部
// 经 D2D 直绘。二维码不落位图：按模块矩阵画矩形，任意 DPI 下都锐利。

// 扫码窗布局常量（96dpi 逻辑单位）。
const (
	matQRCard    = float32(372.0) // 白卡边长（340 二维码 + 两侧各 16 留白）
	matQRMarginL = float32(24.0)
	matQRMarginR = float32(20.0)
	matQRMarginT = float32(18.0)
)

// ensureQRWindowMaterial 创建 Acrylic 版扫码窗；失败返回 false 回退实色。
func (t *Tray) ensureQRWindowMaterial(dark bool) bool {
	pal, _ := currentPalette()
	var w *walk.MainWindow
	var host *walk.Composite
	err := MainWindow{
		AssignTo:   &w,
		Title:      "AirType · 扫码绑定",
		Size:       Size{Width: 420, Height: 520},
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
		t.cfg.Logger.Error("创建材质扫码窗失败", "err", err)
		return false
	}
	t.qrWin = w
	t.qrDark = dark
	win.MakeBorderlessRoundedPopup(uintptr(w.Handle()))
	// 整面拖动（子画面外的区域按标题栏命中）
	_ = win.SubclassWindow(uintptr(w.Handle()), func(msg uint32, wp, lp uintptr) (bool, uintptr) {
		if msg == 0x0084 { // WM_NCHITTEST
			return true, 2 // HTCAPTION
		}
		return false, 0
	})
	enableMaterialBackdrop(uintptr(w.Handle()), true) // Acrylic

	mat, err := newMatCanvas(host, pal, t.paintQRMaterial)
	mat.onErr = func(err error) { t.cfg.Logger.Error("材质渲染器创建失败", "err", err) }
	if err != nil {
		t.cfg.Logger.Error("创建材质画布失败", "err", err)
		return false
	}
	t.qrMat = mat
	mat.trackHover(func() { mat.redraw() })
	mat.w.MouseMove().Attach(func(x, y int, _ walk.MouseButton) {
		cx0, cy0, cw, ch := t.qrCloseRect()
		inside := float32(x) >= cx0 && float32(x) < cx0+cw && float32(y) >= cy0 && float32(y) < cy0+ch
		if inside != t.qrCloseHover {
			t.qrCloseHover = inside
			mat.redraw()
		}
	})
	mat.w.MouseDown().Attach(func(x, y int, button walk.MouseButton) {
		if button != walk.LeftButton {
			return
		}
		cx0, cy0, cw, ch := t.qrCloseRect()
		if float32(x) >= cx0 && float32(x) < cx0+cw && float32(y) >= cy0 && float32(y) < cy0+ch {
			w.Close() // 走 Closing：隐藏 + QRDismissed
		}
	})
	// 点 ✕ 只隐藏不销毁；QQ 绑定流程据此取消
	w.Closing().Attach(func(canceled *bool, reason walk.CloseReason) {
		*canceled = true
		w.Hide()
		if t.cfg.QRDismissed != nil {
			go t.cfg.QRDismissed()
		}
	})
	if !t.matQREnsureRenderer() {
		return false
	}
	return true
}

func (t *Tray) matQREnsureRenderer() bool {
	if t.qrMat.r != nil {
		return true
	}
	pw, ph := win.ClientRectPixels(uintptr(t.qrWin.Handle()))
	if pw == 0 || ph == 0 {
		return false
	}
	r, err := win.NewD2DRenderer(uintptr(t.qrWin.Handle()), pw, ph)
	if err != nil {
		t.cfg.Logger.Error("创建 D2D 渲染器失败，回退实色", "err", err)
		return false
	}
	t.qrMat.r = r
	return true
}

func (t *Tray) qrCloseRect() (x, y, w, h float32) {
	if t.qrMat != nil && t.qrMat.r != nil {
		lw, _ := t.qrMat.r.Size()
		return lw - matQRMarginR - matCloseW, matQRMarginT, matCloseW, matCloseH
	}
	return 366, 18, matCloseW, matCloseH
}

func (t *Tray) paintQRMaterial(r *win.D2DRenderer, w, h float32) error {
	pal := t.qrMat.pal
	r.BeginDraw()

	_ = r.DrawText(matQRMarginL, matQRMarginT, w-100, matCloseH, t.qrTitleStr,
		win.DrawTextOpts{Size: 12, Bold: true, Color: uint32(pal.Text), VCenter: true})
	t.matQRDrawClose(r, w)
	_ = r.DrawText(matQRMarginL, matQRMarginT+matCloseH+8, w-matQRMarginL-matQRMarginR, 18,
		t.qrHintStr, win.DrawTextOpts{Size: 9, Color: uint32(pal.TextSecondary), Ellipsis: true})

	// 白卡 + 二维码模块（模块矩阵按需生成）
	cardX := (w - matQRCard) / 2
	cardY := matQRMarginT + matCloseH + 8 + 18 + 24
	r.FillRoundedRect(cardX, cardY, matQRCard, matQRCard, 8, 0xFFFFFF, 1)
	matrix := t.qrMatrixOf()
	if matrix != nil {
		n := float32(len(matrix))
		const quiet = 4.0
		m := (matQRCard - 32.0) / (n + 8.0) // 16 两侧留白 + 每侧 4 模块静区
		x0 := cardX + 16 + quiet*m
		y0 := cardY + 16 + quiet*m
		for row := range matrix {
			for col := range matrix[row] {
				if matrix[row][col] {
					r.FillRect(x0+float32(col)*m, y0+float32(row)*m, m, m, 0x000000, 1)
				}
			}
		}
	}
	return r.EndDraw()
}

func (t *Tray) matQRDrawClose(r *win.D2DRenderer, w float32) {
	cx, cy, cw, ch := t.qrCloseRect()
	cx, cy = cx+cw/2, cy+ch/2
	if t.qrCloseHover {
		r.FillEllipse(cx, cy, 12, 12, uint32(t.qrMat.pal.Hover), 1)
	}
	color := t.qrMat.pal.TextSecondary
	if t.qrCloseHover {
		color = t.qrMat.pal.Text
	}
	r.DrawLine(cx-4, cy-4, cx+4, cy+4, 1.2, uint32(color), 1)
	r.DrawLine(cx-4, cy+4, cx+4, cy-4, 1.2, uint32(color), 1)
}

// qrMatrixOf 生成（并缓存）当前链接的二维码模块矩阵。
func (t *Tray) qrMatrixOf() [][]bool {
	if t.qrMatrix != nil {
		return t.qrMatrix
	}
	if t.qrURL == "" {
		return nil
	}
	qr, err := qrcode.New(t.qrURL, qrcode.Medium)
	if err != nil {
		t.cfg.Logger.Error("生成二维码失败", "err", err)
		return nil
	}
	t.qrMatrix = qr.Bitmap()
	return t.qrMatrix
}
