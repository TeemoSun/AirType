//go:build windows

package ui

import (
	"github.com/lxn/walk"

	"github.com/TeemoSun/AirType/internal/win"
)

const wmMouseLeave = 0x02A2

// hoverTracker 管理自绘控件的悬停/按压标记。walk 的 CustomWidget 不发
// MouseLeave 事件：鼠标进入时用 TrackMouseEvent 预约一次 WM_MOUSELEAVE，
// 由通用子类化（win.SubclassWindowProc）接住后清除标记。
type hoverTracker struct {
	w         *walk.CustomWidget
	hover     bool
	pressed   bool
	tracking  bool
	uninstall func()
}

// attach 挂到控件上（须在控件句柄创建后调用）。
func (h *hoverTracker) attach(w *walk.CustomWidget) error {
	h.w = w
	un, err := win.SubclassWindowProc(uintptr(w.Handle()), func(msg uint32, _, _ uintptr) (bool, uintptr) {
		if msg == wmMouseLeave {
			h.tracking = false
			if h.hover || h.pressed {
				h.hover, h.pressed = false, false
				w.Invalidate()
			}
		}
		return false, 0
	})
	if err != nil {
		return err
	}
	h.uninstall = un
	w.MouseMove().Attach(func(x, y int, _ walk.MouseButton) {
		h.hover = true
		if !h.tracking {
			h.tracking = true
			win.TrackMouseLeave(uintptr(w.Handle()))
		}
		w.Invalidate()
	})
	return nil
}

// channelCard 是通道选择窗的自绘卡片：圆角 Card 底 + 1px 描边，悬停描边
// 变系统强调色、按压压暗底色，标题 + 两行说明。整卡就是一个真实 HWND
// 控件，命中区=控件边界，walk 事件路由原样生效——不做任何手工命中测试。
type channelCard struct {
	w        *walk.CustomWidget
	pal      Palette
	title    string
	subs     []string
	ht       hoverTracker
	onClick  func()
	fntTitle *walk.Font
	fntSub   *walk.Font
	brWindow *walk.SolidColorBrush
}

// newChannelCard 在 host 容器内创建卡片（host 须已设 Layout 且给出
// MinSize；卡片纵向横向铺满 host）。
func newChannelCard(host walk.Container, title string, subs []string, onClick func()) (*channelCard, error) {
	pal, _ := currentPalette()
	c := &channelCard{title: title, subs: subs, onClick: onClick, pal: pal}
	var err error
	if c.fntTitle, err = walk.NewFont(UIFontFamily(), 11, walk.FontBold); err != nil {
		return nil, err
	}
	if c.fntSub, err = walk.NewFont(UIFontFamily(), 8, 0); err != nil {
		return nil, err
	}
	// 自绘控件默认白底擦除，铺窗口底色（深浅色两可）
	if c.brWindow, err = walk.NewSolidColorBrush(pal.Window); err != nil {
		return nil, err
	}
	w, err := walk.NewCustomWidgetPixels(host, 0, c.paint)
	if err != nil {
		return nil, err
	}
	c.w = w
	w.SetBackground(c.brWindow)
	if err := c.ht.attach(w); err != nil {
		return nil, err
	}
	w.MouseDown().Attach(func(x, y int, button walk.MouseButton) {
		if button != walk.LeftButton {
			return
		}
		c.ht.pressed = true
		w.Invalidate()
	})
	w.MouseUp().Attach(func(x, y int, button walk.MouseButton) {
		if button != walk.LeftButton {
			return
		}
		fired := c.ht.pressed
		c.ht.pressed = false
		w.Invalidate()
		if fired && c.onClick != nil {
			c.onClick()
		}
	})
	return c, nil
}

// paint 自绘卡片。几何基准取控件实际客户区（paint 回调的 bounds 是脏区
// 矩形，局部重绘时会偏小），GDI 按脏区裁剪保证结果正确。
func (c *channelCard) paint(canvas *walk.Canvas, _ walk.Rectangle) error {
	bounds := c.w.ClientBoundsPixels()
	dpi := c.w.DPI()

	base, stroke := c.pal.Card, c.pal.Stroke
	switch {
	case c.ht.pressed:
		base, stroke = c.pal.Pressed, c.pal.Stroke
	case c.ht.hover:
		stroke = c.pal.Accent
	}
	if err := strokeRoundRect(canvas, bounds, 8, stroke, base); err != nil {
		return err
	}

	pad := walk.IntFrom96DPI(16, dpi)
	rTitle := walk.Rectangle{
		X:      bounds.X + pad,
		Y:      bounds.Y + walk.IntFrom96DPI(16, dpi),
		Width:  bounds.Width - 2*pad,
		Height: walk.IntFrom96DPI(24, dpi),
	}
	if err := canvas.DrawTextPixels(c.title, c.fntTitle, c.pal.Text, rTitle,
		walk.TextLeft|walk.TextVCenter|walk.TextSingleLine); err != nil {
		return err
	}
	lineH := walk.IntFrom96DPI(17, dpi)
	y := bounds.Y + walk.IntFrom96DPI(48, dpi)
	for i, s := range c.subs {
		rSub := walk.Rectangle{X: bounds.X + pad, Y: y + i*lineH, Width: bounds.Width - 2*pad, Height: lineH}
		if err := canvas.DrawTextPixels(s, c.fntSub, c.pal.TextSecondary, rSub,
			walk.TextLeft|walk.TextVCenter|walk.TextSingleLine); err != nil {
			return err
		}
	}
	return nil
}
