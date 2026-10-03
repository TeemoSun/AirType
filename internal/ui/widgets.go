//go:build windows

package ui

import (
	"github.com/lxn/walk"

	"github.com/TeemoSun/AirType/internal/win"
)

const wmMouseLeave = 0x02A2

// hoverState 管理"鼠标悬停"标记：walk 不发 MouseLeave 事件，进入时通过
// TrackMouseEvent 请求 WM_MOUSELEAVE，再由通用子类化接住清除标记。
// 所有自绘按钮（通道选择、关闭键）共用此机制。
type hoverState struct {
	hover    bool
	tracking bool
}

// attach 挂到自绘控件上：MouseMove 里由调用方置 hover，离开时自动置
// false 并回调 onLeave（须在 UI 线程）。
func (h *hoverState) attach(w *walk.CustomWidget, onLeave func()) {
	hwnd := uintptr(w.Handle())
	w.MouseMove().Attach(func(x, y int, _ walk.MouseButton) {
		h.hover = true
		if !h.tracking {
			h.tracking = true
			win.TrackMouseLeave(hwnd)
		}
		w.Invalidate()
	})
	_ = win.SubclassWindow(hwnd, func(msg uint32, wp, lp uintptr) (bool, uintptr) {
		if msg == wmMouseLeave {
			h.tracking = false
			if h.hover {
				h.hover = false
				if onLeave != nil {
					onLeave()
				}
			}
		}
		return false, 0
	})
}

// newWidgetBrush 建控件底刷并挂到控件上：自绘控件默认白底擦除，必须
// 显式铺主题底色（深色窗口上才是双保险）。paint 里不必再整面填底。
func newWidgetBrush(w *walk.CustomWidget, color walk.Color) (*walk.SolidColorBrush, error) {
	br, err := walk.NewSolidColorBrush(color)
	if err != nil {
		return nil, err
	}
	w.SetBackground(br)
	return br, nil
}

// fluentButton 是 Win11 风格自绘按钮：primary=true 为强调色实底主按钮，
// 否则为卡片底+描边的次级按钮；悬停/按压两态。替代原生 PushButton
// （原生样式停在 Win10 方角，深色下还得靠 DarkMode_Explorer 打补丁）。
type fluentButton struct {
	w       *walk.CustomWidget
	pal     Palette
	text    string
	primary bool
	hst     hoverState
	pressed bool
	onClick func()
	fnt     *walk.Font
}

// newFluentButton 在 host Composite 内创建按钮（host 须已声明固定高度，
// 按钮横向铺满 host）。控件圆角外的四角由控件底刷（= 窗口底色）补齐。
func newFluentButton(host walk.Container, text string, primary bool, onClick func()) (*fluentButton, error) {
	pal, _ := currentPalette()
	b := &fluentButton{text: text, primary: primary, onClick: onClick, pal: pal}
	var err error
	if b.fnt, err = walk.NewFont(UIFontFamily(), 10, 0); err != nil {
		return nil, err
	}
	w, err := walk.NewCustomWidgetPixels(host, 0, b.paint)
	if err != nil {
		return nil, err
	}
	b.w = w
	if _, err = newWidgetBrush(w, pal.Window); err != nil {
		return nil, err
	}
	w.MouseDown().Attach(func(x, y int, button walk.MouseButton) {
		if button == walk.LeftButton {
			b.pressed = true
			w.Invalidate()
		}
	})
	w.MouseUp().Attach(func(x, y int, button walk.MouseButton) {
		if button != walk.LeftButton {
			return
		}
		was := b.pressed
		b.pressed = false
		w.Invalidate()
		// 按住拖出后松开不触发点击（标准按钮语义）
		if was && x >= 0 && y >= 0 && x < w.BoundsPixels().Width && y < w.BoundsPixels().Height {
			if b.onClick != nil {
				b.onClick()
			}
		}
	})
	b.hst.attach(w, func() { w.Invalidate() })
	return b, nil
}

func (b *fluentButton) paint(canvas *walk.Canvas, bounds walk.Rectangle) error {
	dpi := b.w.DPI()
	radius := walk.IntFrom96DPI(4, dpi)
	ell := walk.Size{Width: radius * 2, Height: radius * 2}

	var fill walk.Color
	if b.primary {
		switch {
		case b.pressed:
			fill = b.pal.AccentPressed
		case b.hst.hover:
			fill = b.pal.AccentHover
		default:
			fill = b.pal.Accent
		}
	} else {
		switch {
		case b.pressed:
			fill = b.pal.Pressed
		case b.hst.hover:
			fill = b.pal.Hover
		default:
			fill = b.pal.Card
		}
	}
	brush, err := walk.NewSolidColorBrush(fill)
	if err != nil {
		return err
	}
	defer brush.Dispose()
	if err := canvas.FillRoundedRectanglePixels(brush, bounds, ell); err != nil {
		return err
	}
	if !b.primary {
		pen, err := walk.NewCosmeticPen(walk.PenSolid, b.pal.Stroke)
		if err != nil {
			return err
		}
		defer pen.Dispose()
		if err := canvas.DrawRoundedRectanglePixels(pen, bounds, ell); err != nil {
			return err
		}
	}
	txtColor := b.pal.Text
	if b.primary {
		txtColor = b.pal.OnAccent
	}
	return canvas.DrawTextPixels(b.text, b.fnt, txtColor, bounds,
		walk.TextCenter|walk.TextVCenter|walk.TextSingleLine)
}

// closeButton 是自绘关闭键 ✕：悬停时画圆形底（Win11 标题栏键的行为）。
// popup 头部与扫码窗共用。
type closeButton struct {
	w   *walk.CustomWidget
	pal Palette
	hst hoverState
}

func newCloseButton(host walk.Container, onClick func()) (*closeButton, error) {
	pal, _ := currentPalette()
	c := &closeButton{pal: pal}
	w, err := walk.NewCustomWidgetPixels(host, 0, c.paint)
	if err != nil {
		return nil, err
	}
	c.w = w
	if _, err = newWidgetBrush(w, pal.Window); err != nil {
		return nil, err
	}
	w.MouseDown().Attach(func(x, y int, button walk.MouseButton) {
		if button == walk.LeftButton && onClick != nil {
			onClick()
		}
	})
	c.hst.attach(w, func() { w.Invalidate() })
	return c, nil
}

func (c *closeButton) paint(canvas *walk.Canvas, bounds walk.Rectangle) error {
	if c.hst.hover {
		hb, err := walk.NewSolidColorBrush(c.pal.Hover)
		if err != nil {
			return err
		}
		defer hb.Dispose()
		d := bounds.Width
		if bounds.Height < d {
			d = bounds.Height
		}
		r := walk.Rectangle{X: bounds.X + (bounds.Width-d)/2, Y: bounds.Y + (bounds.Height-d)/2, Width: d, Height: d}
		if err := canvas.FillEllipsePixels(hb, r); err != nil {
			return err
		}
	}
	color := c.pal.TextSecondary
	if c.hst.hover {
		color = c.pal.Text
	}
	pen, err := walk.NewCosmeticPen(walk.PenSolid, color)
	if err != nil {
		return err
	}
	defer pen.Dispose()
	cx, cy := bounds.X+bounds.Width/2, bounds.Y+bounds.Height/2
	dd := 4
	if err := canvas.DrawLinePixels(pen, walk.Point{X: cx - dd, Y: cy - dd}, walk.Point{X: cx + dd, Y: cy + dd}); err != nil {
		return err
	}
	return canvas.DrawLinePixels(pen, walk.Point{X: cx - dd, Y: cy + dd}, walk.Point{X: cx + dd, Y: cy - dd})
}
