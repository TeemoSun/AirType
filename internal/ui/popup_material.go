//go:build windows

package ui

import (
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"

	"github.com/TeemoSun/AirType/internal/win"
)

// 历史弹窗·材质呈现（方案 §10.7）：Acrylic 瞬态窗，全部内容经 D2D 直绘。
// 状态（条目/悬停/选中/滚动/复制反馈）与逻辑（复制/删除/重打/失焦监视）
// 与实色呈现完全共用（见 popup.go），这里只负责"画出来"和"点得到"。

// matListState 材质呈现下的列表视口状态（与 fluentList 同语义）。
type matListState struct {
	items   []fluentItem
	hover   int
	sel     int
	scroll  int
	visRows int
}

// 布局常量（96dpi 逻辑单位，与实色呈现的 flRowH96 等一致）。
const (
	matHeaderH   = float32(46.0) // 14 + 24 + 8
	matFooterH   = float32(36.0) // 8 + 16 + 12
	matBadgeW    = float32(136.0)
	matBadgeH    = float32(24.0)
	matCloseW    = float32(30.0)
	matCloseH    = float32(24.0)
	matRowH      = float32(flRowH96)
	matInset     = float32(6.0)
	matPad       = float32(flTextPad96)
	matScrollbar = float32(3.0)
)

// ensureHistoryPopupMaterial 创建材质版历史弹窗；任何一步失败返回 false，
// 调用方回退实色呈现。
func (t *Tray) ensureHistoryPopupMaterial(dark bool) bool {
	pal, _ := currentPalette()
	p := &historyPopup{tray: t, pal: pal}
	p.matList = &matListState{hover: -1, sel: -1}

	var w *walk.MainWindow
	var host *walk.Composite
	err := MainWindow{
		AssignTo:   &w,
		Title:      "AirType 历史",
		Size:       Size{Width: 400, Height: 520},
		Background: SolidColorBrush{Color: pal.Window},
		Layout:     VBox{MarginsZero: true, SpacingZero: true},
		OnKeyDown: func(key walk.Key) {
			p.matKeyDown(key)
		},
		Children: []Widget{
			Composite{
				AssignTo:      &host,
				Layout:        VBox{MarginsZero: true, SpacingZero: true},
				StretchFactor: 1,
			},
		},
	}.Create()
	if err != nil {
		t.cfg.Logger.Error("创建材质历史弹窗失败", "err", err)
		return false
	}
	p.win = w
	win.MakeTopmostToolWindow(uintptr(w.Handle()))
	win.MakeBorderlessRoundedPopup(uintptr(w.Handle()))
	enableMaterialBackdrop(uintptr(w.Handle()), true) // Acrylic（瞬态窗语义）

	mat, err := newMatCanvas(host, pal, p.paintMaterial)
	mat.onErr = func(err error) { t.cfg.Logger.Error("材质渲染器创建失败", "err", err) }
	if err != nil {
		t.cfg.Logger.Error("创建材质画布失败", "err", err)
		return false
	}
	p.mat = mat
	mat.trackHover(func() { mat.redraw() })
	mat.w.MouseMove().Attach(p.matMouseMove)
	mat.w.MouseDown().Attach(p.matMouseDown)
	mat.w.MouseWheel().Attach(p.matMouseWheel)
	mat.w.KeyDown().Attach(p.matKeyDown)

	if !p.matEnsureRenderer() {
		return false
	}

	t.popup = p
	t.popupDark = dark
	return true
}

// matEnsureRenderer 惰性创建渲染器（窗口可见后客户区尺寸才真实）。
func (p *historyPopup) matEnsureRenderer() bool {
	if p.mat.r != nil {
		return true
	}
	pw, ph := win.ClientRectPixels(uintptr(p.win.Handle()))
	if pw == 0 || ph == 0 {
		return false
	}
	r, err := win.NewD2DRenderer(uintptr(p.win.Handle()), pw, ph)
	if err != nil {
		p.tray.cfg.Logger.Error("创建 D2D 渲染器失败，回退实色", "err", err)
		return false
	}
	p.mat.r = r
	return true
}

// ---- 绘制（96dpi 逻辑坐标）----

func (p *historyPopup) paintMaterial(r *win.D2DRenderer, w, h float32) error {
	pal := p.pal
	r.BeginDraw()

	// 头部：产品名 + 状态胶囊 + 关闭键
	r.DrawText(16, 11, 150, matBadgeH, "AirType",
		win.DrawTextOpts{Size: 12, Bold: true, Color: uint32(pal.Text), VCenter: true})
	bx := w - 12 - matCloseW - 8 - matBadgeW
	// 胶囊：外圈描边色 + 内芯卡片色（双填充画 1px 描边）。
	// 不用 DrawRoundedRectangle（槽位 18）——本机构建上它毒化整帧
	// （EndDraw 返回 D2DERR_RECREATE_TARGET），已实测，见开发方案 §10.7。
	r.FillRoundedRect(bx-1, 10, matBadgeW+2, matBadgeH+2, 13, uint32(pal.Stroke), 1)
	r.FillRoundedRect(bx, 11, matBadgeW, matBadgeH, 12, uint32(pal.Card), 1)
	r.FillEllipse(bx+14, 11+12, 4, 4, uint32(p.dotColor()), 1)
	_ = r.DrawText(bx+24, 11, matBadgeW-24-6, matBadgeH, p.statusText(),
		win.DrawTextOpts{Size: 9, Color: uint32(pal.TextSecondary), VCenter: true, Ellipsis: true})
	p.matDrawClose(r, w)

	// 列表区
	listTop, listH := matHeaderH, h-matHeaderH-matFooterH
	items := p.matList.items
	rowH := float32(matRowH)
	// 底部提示（空态分支提前返回，故先画）
	_ = r.DrawText(16, h-matFooterH+8, w-32, 16, "单击复制 · 右键复制/删除/重新打字",
		win.DrawTextOpts{Size: 8, Color: uint32(pal.TextSecondary)})
	if len(items) == 0 {
		_ = r.DrawText(0, listTop+listH/2-10, w, 20, p.matEmptyText(),
			win.DrawTextOpts{Size: 10, Color: uint32(pal.TextSecondary), HCenter: true})
		return p.matEndDraw()
	}
	p.matList.visRows = int(listH / rowH)
	p.matClampScroll()

	y := listTop
	for i := p.matList.scroll; i < len(items) && y+rowH <= listTop+listH; i++ {
		it := items[i]
		isSel := i == p.matList.sel
		isHover := i == p.matList.hover
		if isSel || isHover {
			color := pal.Hover
			if isSel {
				color = pal.Selection
			}
			r.FillRoundedRect(matInset, y+2, w-matInset*2, rowH-4, 4, uint32(color), 1)
		}
		if isSel {
			pillH := float32(rowH - 16)
			r.FillRoundedRect(matInset+4, y+(rowH-pillH)/2, 3, pillH, 1.5, uint32(pal.Accent), 1)
		}
		_ = r.DrawText(matPad, y+9, w-matPad-16, 20, it.text,
			win.DrawTextOpts{Size: 10, Color: uint32(pal.Text), VCenter: true, Ellipsis: true, ColorEmoji: true})
		line2, col := it.timeStr, pal.TextSecondary
		if it.id == p.copiedID && time.Since(p.copiedAt) < flCopyFlash {
			line2, col = "已复制 ✓", pal.Flash
		}
		_ = r.DrawText(matPad, y+31, w-matPad-16, 16, line2,
			win.DrawTextOpts{Size: 8, Color: uint32(col), VCenter: true})
		y += rowH
	}

	// 迷你滚动条
	if total := len(items); total > p.matList.visRows && p.matList.visRows > 0 {
		thumbH := listH * float32(p.matList.visRows) / float32(total)
		if thumbH < 24 {
			thumbH = 24
		}
		maxScroll := total - p.matList.visRows
		thumbY := float32(0)
		if maxScroll > 0 {
			thumbY = (listH - thumbH) * float32(p.matList.scroll) / float32(maxScroll)
		}
		r.FillRoundedRect(w-matInset-matScrollbar, listTop+thumbY, matScrollbar, thumbH, 1.5,
			uint32(pal.ScrollThumb), 1)
	}

	// 底部提示
	return p.matEndDraw()
}

// matEndDraw 结束一帧（首次失败留日志）。
func (p *historyPopup) matEndDraw() error {
	if err := p.mat.r.EndDraw(); err != nil {
		if !p.matEndLogged {
			p.matEndLogged = true
			p.tray.cfg.Logger.Error("材质弹窗 EndDraw 失败", "err", err)
		}
		return err
	}
	return nil
}

func (p *historyPopup) matDrawClose(r *win.D2DRenderer, w float32) {
	cx := w - 12 - matCloseW/2
	cy := 11 + matCloseH/2
	hover := p.matCloseHover
	if hover {
		r.FillEllipse(cx, cy, 12, 12, uint32(p.pal.Hover), 1)
	}
	color := p.pal.TextSecondary
	if hover {
		color = p.pal.Text
	}
	r.DrawLine(cx-4, cy-4, cx+4, cy+4, 1.2, uint32(color), 1)
	r.DrawLine(cx-4, cy+4, cx+4, cy-4, 1.2, uint32(color), 1)
}

// matCloseRect 关闭键命中区（逻辑坐标）。
func (p *historyPopup) matCloseRect(w float32) (x, y, cw, ch float32) {
	return w - 12 - matCloseW, 11, matCloseW, matCloseH
}

// ---- 交互 ----

func (p *historyPopup) matMouseMove(x, y int, _ walk.MouseButton) {
	w := p.matLogicalWidth()
	row := p.matRowAt(float32(y))
	if row != p.matList.hover {
		p.matList.hover = row
		if row >= 0 && row < len(p.matList.items) {
			p.mat.w.SetToolTipText(p.matList.items[row].full)
		} else {
			p.mat.w.SetToolTipText("")
		}
		p.mat.redraw()
	}
	chx, chy, cw, ch := p.matCloseRect(w)
	inside := float32(x) >= chx && float32(x) < chx+cw && float32(y) >= chy && float32(y) < chy+ch
	if inside != p.matCloseHover {
		p.matCloseHover = inside
		p.mat.redraw()
	}
}

func (p *historyPopup) matMouseDown(x, y int, button walk.MouseButton) {
	w := p.matLogicalWidth()
	chx, chy, cw, ch := p.matCloseRect(w)
	if float32(x) >= chx && float32(x) < chx+cw && float32(y) >= chy && float32(y) < chy+ch {
		if button == walk.LeftButton {
			p.hide()
		}
		return
	}
	row := p.matRowAt(float32(y))
	if row < 0 || row >= len(p.matList.items) {
		return
	}
	id := p.matList.items[row].id
	if button == walk.LeftButton {
		p.matList.sel = row
		p.mat.redraw()
		p.copyAt(id)
		return
	}
	if button == walk.RightButton {
		choice := win.ShowContextMenu(uintptr(p.win.Handle()), []string{"复制", "删除", "重新打字"})
		switch choice {
		case 0:
			p.copyAt(id)
		case 1:
			if p.tray.cfg.History != nil {
				p.tray.cfg.History.Delete(id)
				p.reload()
			}
		case 2:
			p.retype(id)
		}
	}
}

func (p *historyPopup) matMouseWheel(x, y int, button walk.MouseButton) {
	delta := walk.MouseWheelEventDelta(button)
	if delta == 0 {
		return
	}
	rows := -delta / 40
	if rows == 0 {
		rows = 1
	}
	p.matList.scroll += rows
	p.matClampScroll()
	p.mat.redraw()
}

func (p *historyPopup) matKeyDown(key walk.Key) {
	switch key {
	case walk.KeyUp, walk.KeyDown:
		if len(p.matList.items) == 0 {
			return
		}
		d := 1
		if key == walk.KeyUp {
			d = -1
		}
		if p.matList.sel < 0 {
			p.matList.sel = p.matList.scroll
		} else {
			p.matList.sel += d
		}
		if p.matList.sel < 0 {
			p.matList.sel = 0
		}
		if p.matList.sel >= len(p.matList.items) {
			p.matList.sel = len(p.matList.items) - 1
		}
		// ensureVisible
		if p.matList.visRows > 0 {
			if p.matList.sel < p.matList.scroll {
				p.matList.scroll = p.matList.sel
			}
			if p.matList.sel >= p.matList.scroll+p.matList.visRows {
				p.matList.scroll = p.matList.sel - p.matList.visRows + 1
			}
			p.matClampScroll()
		}
		p.mat.redraw()
	case walk.KeyReturn:
		if p.matList.sel >= 0 && p.matList.sel < len(p.matList.items) {
			p.copyAt(p.matList.items[p.matList.sel].id)
		}
	case walk.KeyDelete:
		if p.matList.sel >= 0 && p.matList.sel < len(p.matList.items) && p.tray.cfg.History != nil {
			p.tray.cfg.History.Delete(p.matList.items[p.matList.sel].id)
			p.matList.sel = -1
			p.reload()
		}
	case walk.KeyEscape:
		p.hide()
	}
}

// ---- 状态辅助 ----

func (p *historyPopup) matRowAt(y float32) int {
	if y < matHeaderH {
		return -1
	}
	rowH := float32(matRowH)
	idx := p.matList.scroll + int((y-matHeaderH)/rowH)
	listH := p.matLogicalHeight() - matHeaderH - matFooterH
	if y >= matHeaderH+listH || idx >= len(p.matList.items) || (y-matHeaderH) >= listH {
		return -1
	}
	return idx
}

func (p *historyPopup) matClampScroll() {
	l := p.matList
	if l.visRows > 0 {
		if max := len(l.items) - l.visRows; l.scroll > max {
			l.scroll = max
		}
	}
	if l.scroll > len(l.items)-1 {
		l.scroll = len(l.items) - 1
	}
	if l.scroll < 0 {
		l.scroll = 0
	}
}

func (p *historyPopup) matLogicalWidth() float32 {
	if p.mat == nil || p.mat.r == nil {
		return 400
	}
	w, _ := p.mat.r.Size()
	return w
}

func (p *historyPopup) matLogicalHeight() float32 {
	if p.mat == nil || p.mat.r == nil {
		return 520
	}
	_, h := p.mat.r.Size()
	return h
}

func (p *historyPopup) matEmptyText() string {
	switch p.tray.mode {
	case ChannelQQ:
		return "暂无消息，用手机 QQ 发一条试试"
	default:
		return "暂无消息，绑定通道后用手机发一条试试"
	}
}
