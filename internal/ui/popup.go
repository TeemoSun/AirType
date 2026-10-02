//go:build windows

package ui

import (
	"fmt"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"

	"github.com/TeemoSun/AirType/internal/history"
	"github.com/TeemoSun/AirType/internal/win"
)

// relTime 生成相对时间文案。
func relTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "刚刚"
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟前", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d 小时前", int(d.Hours()))
	case d < 48*time.Hour:
		return "昨天 " + t.Format("15:04")
	default:
		return t.Format("01-02 15:04")
	}
}

// clampText 截断文本到最多 maxRunes 个 rune，换行折叠为空格。
func clampText(s string, maxRunes int) string {
	out := make([]rune, 0, maxRunes)
	n := 0
	for _, r := range s {
		if r == '\n' || r == '\r' {
			r = ' '
		}
		if n == maxRunes {
			out = append(out, '…')
			break
		}
		out = append(out, r)
		n++
	}
	return string(out)
}

// 配色（Fluent 浅色主题）
var (
	colWhite    = walk.RGB(255, 255, 255)
	colZebra    = walk.RGB(245, 246, 247)
	colHover    = walk.RGB(233, 235, 238)
	colText     = walk.RGB(26, 28, 32)
	colTextGray = walk.RGB(138, 143, 150)
	colDotGreen = walk.RGB(34, 197, 94)
	colDotRed   = walk.RGB(239, 68, 68)
	colDotGray  = walk.RGB(156, 163, 175)
)

// historyPopup 是 OneDrive 风格的无边框圆角历史浮层（方案 §4.1）。
type historyPopup struct {
	tray      *Tray
	win       *walk.MainWindow
	list      *fluentList
	closeBtn  *closeButton
	snapshot  win.FocusSnapshot
	shownAt   time.Time
	statusLbl *walk.Label
}

func (t *Tray) ensureHistoryPopup() {
	if t.popup != nil {
		return
	}
	p := &historyPopup{tray: t}
	var w *walk.MainWindow
	var status *walk.Label
	var listHost, closeHost *walk.Composite

	err := MainWindow{
		AssignTo:   &w,
		Title:      "AirType 历史",
		Size:       Size{Width: 400, Height: 520},
		Background: SolidColorBrush{Color: colWhite},
		Layout:     VBox{MarginsZero: true, SpacingZero: true},
		OnKeyDown: func(key walk.Key) {
			if key == walk.KeyEscape {
				p.hide()
			}
		},
		Children: []Widget{
			// 标题行：大标题 + 状态点 + 关闭按钮
			Composite{
				Background: SolidColorBrush{Color: colWhite},
				Layout:     HBox{Margins: Margins{Left: 20, Top: 18, Right: 12, Bottom: 10}},
				Children: []Widget{
					Label{Text: "隔空打字", Font: Font{Family: "Segoe UI", PointSize: 15, Bold: true}},
					Label{AssignTo: &status, Text: "●", TextColor: colDotGray},
					HSpacer{},
					Composite{AssignTo: &closeHost, MinSize: Size{Width: 36, Height: 36}},
				},
			},
			// 列表宿主：自绘 Fluent 列表挂到这里
			Composite{
				AssignTo:   &listHost,
				Background: SolidColorBrush{Color: colWhite},
				Layout:     VBox{MarginsZero: true, SpacingZero: true},
			},
			// 底部提示
			Composite{
				Background: SolidColorBrush{Color: colWhite},
				Layout:     HBox{Margins: Margins{Left: 20, Top: 8, Right: 20, Bottom: 12}},
				Children: []Widget{
					Label{Text: "单击复制 · 右键复制/删除", TextColor: colTextGray},
					HSpacer{},
				},
			},
		},
	}.Create()
	if err != nil {
		t.cfg.Logger.Error("创建历史弹窗失败", "err", err)
		return
	}
	p.win = w
	p.statusLbl = status

	// 无边框 + 圆角（Win11 风格浮层）
	win.MakeTopmostToolWindow(uintptr(w.Handle()))
	win.MakeBorderlessRoundedPopup(uintptr(w.Handle()))

	fl, err := newFluentList(listHost, p)
	if err != nil {
		t.cfg.Logger.Error("创建列表失败", "err", err)
		return
	}
	p.list = fl

	// ✕ 关闭按钮：自绘（walk Label 是 STATIC 控件收不到点击，不能用）
	cb := newCloseButton(closeHost, p)
	p.closeBtn = cb

	t.popup = p
}

// updateHeader 按连接状态更新标题行状态点颜色（须在 UI 线程调用）。
func (p *historyPopup) updateHeader(s TrayState) {
	if p.statusLbl == nil {
		return
	}
	c := colDotGray
	switch s {
	case StateConnected:
		c = colDotGreen
	case StateNeedQR, StateDisconnected:
		c = colDotRed
	}
	p.statusLbl.SetTextColor(c)
}

// show 显示弹窗：主动激活（防被失焦守卫误杀）、锚定鼠标所在显示器工作区右下角。
func (p *historyPopup) show() {
	p.reload()
	p.updateHeader(p.tray.state)
	p.win.Show()
	p.anchor()
	if p.list != nil {
		p.list.Focus()
	}
	p.shownAt = time.Now()
	p.tray.popupVisible.Store(true)
	go func() {
		_ = win.Activate(win.Hwnd(p.win.Handle()), 1200*time.Millisecond)
	}()
	go p.watchFocusLoss()
}

// anchor 用窗口"实际像素尺寸"（已含 DPI 缩放）做右下角锚定并钳制。
func (p *historyPopup) anchor() {
	b := p.win.BoundsPixels()
	rc := win.WorkAreaAtCursor()
	x := int(rc.X) + int(rc.Width) - b.Width - 12
	y := int(rc.Y) + int(rc.Height) - b.Height - 8
	if x < int(rc.X) {
		x = int(rc.X)
	}
	if y < int(rc.Y) {
		y = int(rc.Y)
	}
	_ = p.win.SetBoundsPixels(walk.Rectangle{X: x, Y: y, Width: b.Width, Height: b.Height})
}

func (p *historyPopup) hide() {
	p.tray.popupVisible.Store(false)
	p.win.Hide()
}

func (p *historyPopup) reload() {
	if p.tray.cfg.History == nil || p.list == nil {
		return
	}
	entries := p.tray.cfg.History.All()
	p.list.setItems(entries)
}

// copyAt 复制指定条目并气泡确认。
func (p *historyPopup) copyAt(i int) {
	entries := p.tray.cfg.History.All()
	if i < 0 || i >= len(entries) {
		return
	}
	p.tray.copyToClipboard(entries[i].Text)
	_ = p.tray.ni.ShowInfo("AirType", "已复制到剪贴板")
}

// watchFocusLoss 失焦自动关闭（带回退容错）：
//  - 弹窗曾获得前台 → 失去前台即关闭候选；但若前台回到打开前的窗口且
//    距激活不足 1.2s，视为系统前台权限瞬时回退（托盘点击的激活权很短），
//    不关闭——否则弹窗闪关，表现为"打不开"；
//  - 切到任何第三方窗口 → 立即关闭；
//  - 上下文菜单（#32768）持有前台时不算失焦。
func (p *historyPopup) watchFocusLoss() {
	popupHwnd := uintptr(p.win.Handle())
	preFg := uintptr(p.snapshot.Foreground)
	wasActivated := false
	var activatedAt time.Time
	for range time.Tick(250 * time.Millisecond) {
		if !p.win.Visible() {
			return
		}
		fg := uintptr(win.Foreground())
		if fg == popupHwnd {
			wasActivated = true
			activatedAt = time.Now()
			continue
		}
		if win.ForegroundClassName() == "#32768" {
			continue
		}
		if fg == preFg {
			// 激活后 1.2s 内回到原窗口：系统回退，保持弹窗
			if wasActivated && time.Since(activatedAt) > 1200*time.Millisecond {
				p.tray.mw.Synchronize(func() { p.hide() })
				return
			}
			continue
		}
		if fg != 0 {
			p.tray.mw.Synchronize(func() { p.hide() })
			return
		}
	}
}

// closeButton 是自绘的 ✕ 关闭按钮（36×36，悬停淡红高亮）。
// walk 的 Label 是 STATIC 控件、收不到鼠标事件，故必须自绘。
type closeButton struct {
	w     *walk.CustomWidget
	p     *historyPopup
	hover bool
	fnt   *walk.Font
	br    *walk.SolidColorBrush
}

func newCloseButton(parent walk.Container, p *historyPopup) *closeButton {
	cb := &closeButton{p: p}
	var err error
	if cb.fnt, err = walk.NewFont("Segoe UI", 9, 0); err != nil {
		return nil
	}
	if cb.br, err = walk.NewSolidColorBrush(walk.RGB(250, 235, 235)); err != nil {
		return nil
	}
	w, err := walk.NewCustomWidgetPixels(parent, 0, cb.paint)
	if err != nil {
		return nil
	}
	cb.w = w
	w.MouseMove().Attach(func(x, y int, _ walk.MouseButton) {
		if !cb.hover {
			cb.hover = true
			w.Invalidate()
		}
	})
	w.MouseDown().Attach(func(x, y int, button walk.MouseButton) {
		if button == walk.LeftButton {
			p.hide()
		}
	})
	return cb
}

func (cb *closeButton) paint(canvas *walk.Canvas, bounds walk.Rectangle) error {
	color := colTextGray
	if cb.hover {
		if err := canvas.FillRectanglePixels(cb.br, bounds); err != nil {
			return err
		}
		color = walk.RGB(200, 40, 40)
	}
	return canvas.DrawTextPixels("✕", cb.fnt, color, bounds,
		walk.TextCenter|walk.TextVCenter|walk.TextSingleLine)
}

// fluentItem 是列表条目。
type fluentItem struct {
	id      int64
	text    string
	timeStr string
}

// fluentList 是自绘的 Fluent 风格列表：无表头/网格线，斑马纹 + 悬停高亮，
// 每条两行（内容 + 相对时间），滚轮翻页，左键复制、右键原生菜单。
type fluentList struct {
	w       *walk.CustomWidget
	p       *historyPopup
	items   []fluentItem
	hover   int // -1 无
	scroll  int // 第一条可见条目的索引
	rowH96  int

	fntText *walk.Font
	fntTime *walk.Font

	brZebra *walk.SolidColorBrush
	brHover *walk.SolidColorBrush
}

const (
	flRowH96       = 54 // 行高（96dpi 基准）
	flTextPad96    = 18
	flMaxTextRunes = 36
)

func newFluentList(parent walk.Container, p *historyPopup) (*fluentList, error) {
	fl := &fluentList{p: p, hover: -1, rowH96: flRowH96}

	var err error
	if fl.fntText, err = walk.NewFont("Segoe UI", 9, 0); err != nil {
		return nil, err
	}
	if fl.fntTime, err = walk.NewFont("Segoe UI", 8, 0); err != nil {
		return nil, err
	}
	if fl.brZebra, err = walk.NewSolidColorBrush(colZebra); err != nil {
		return nil, err
	}
	if fl.brHover, err = walk.NewSolidColorBrush(colHover); err != nil {
		return nil, err
	}

	w, err := walk.NewCustomWidgetPixels(parent, 0, fl.paint)
	if err != nil {
		return nil, err
	}
	fl.w = w

	w.MouseMove().Attach(fl.onMouseMove)
	w.MouseDown().Attach(fl.onMouseDown)
	w.MouseWheel().Attach(fl.onMouseWheel)
	return fl, nil
}

// Focus 让列表获得键盘焦点（滚轮事件发给焦点窗口）。
func (fl *fluentList) Focus() {
	_ = fl.w.SetFocus()
}

// setItems 更新数据并重绘。
func (fl *fluentList) setItems(entries []history.Entry) {
	fl.items = make([]fluentItem, len(entries))
	for i, e := range entries {
		fl.items[i] = fluentItem{
			id:      e.ID,
			text:    clampText(e.Text, flMaxTextRunes),
			timeStr: relTime(e.ReceivedAt),
		}
	}
	fl.clampScroll()
	fl.hover = -1
	fl.w.Invalidate()
}

func (fl *fluentList) rowHPixels() int {
	return walk.IntFrom96DPI(fl.rowH96, fl.w.DPI())
}

func (fl *fluentList) clampScroll() {
	dpi := fl.w.DPI()
	_ = dpi
	// 可见行数由实际绘制时计算；这里按保守值钳住上界
	if fl.scroll > len(fl.items)-1 {
		fl.scroll = len(fl.items) - 1
	}
	if fl.scroll < 0 {
		fl.scroll = 0
	}
}

// hitTest 返回坐标对应的条目索引，空白处返回 -1（坐标为像素）。
func (fl *fluentList) hitTest(x, y int) int {
	if x < 0 || y < 0 {
		return -1
	}
	idx := fl.scroll + y/fl.rowHPixels()
	if idx < fl.scroll || idx >= len(fl.items) {
		return -1
	}
	return idx
}

func (fl *fluentList) onMouseMove(x, y int, _ walk.MouseButton) {
	idx := fl.hitTest(x, y)
	if idx != fl.hover {
		fl.hover = idx
		fl.w.Invalidate()
	}
}

func (fl *fluentList) onMouseWheel(x, y int, button walk.MouseButton) {
	delta := walk.MouseWheelEventDelta(button)
	if delta == 0 {
		return
	}
	rows := -delta / 40 // 常规一格 120 → 3 行
	if rows == 0 {
		rows = -delta / 120
		if rows == 0 {
			rows = 1
		}
	}
	fl.scroll += rows
	fl.clampScroll()
	fl.w.Invalidate()
}

func (fl *fluentList) onMouseDown(x, y int, button walk.MouseButton) {
	idx := fl.hitTest(x, y)
	if idx < 0 {
		return
	}
	if button == walk.LeftButton {
		fl.p.copyAt(idx)
		return
	}
	if button == walk.RightButton {
		choice := win.ShowContextMenu(uintptr(fl.p.win.Handle()), []string{"复制", "删除"})
		switch choice {
		case 0:
			fl.p.copyAt(idx)
		case 1:
			if fl.p.tray.cfg.History != nil {
				fl.p.tray.cfg.History.Delete(fl.items[idx].id)
				fl.p.reload()
			}
		}
	}
}

// paint 自绘列表内容（坐标均为像素）。
func (fl *fluentList) paint(canvas *walk.Canvas, bounds walk.Rectangle) error {
	dpi := fl.w.DPI()
	rowH := fl.rowHPixels()
	pad := walk.IntFrom96DPI(flTextPad96, dpi)

	if len(fl.items) == 0 {
		txt := "暂无消息，用手机微信发一条试试"
		r := walk.Rectangle{X: 0, Y: bounds.Height/2 - 20, Width: bounds.Width, Height: 40}
		return canvas.DrawTextPixels(txt, fl.fntText, colTextGray, r, walk.TextCenter|walk.TextVCenter|walk.TextSingleLine)
	}

	// 可见行数（用于钳制滚动）
	visRows := bounds.Height / rowH
	if maxScroll := len(fl.items) - visRows; fl.scroll > maxScroll {
		fl.scroll = maxScroll
		if fl.scroll < 0 {
			fl.scroll = 0
		}
	}

	y := 0
	for i := fl.scroll; i < len(fl.items) && y < bounds.Height; i++ {
		it := fl.items[i]
		// 行背景：悬停 > 斑马纹 > 无
		var brush walk.Brush
		if i == fl.hover {
			brush = fl.brHover
		} else if i%2 == 1 {
			brush = fl.brZebra
		}
		if brush != nil {
			rect := walk.Rectangle{X: 6, Y: y + 1, Width: bounds.Width - 12, Height: rowH - 2}
			if err := canvas.FillRectanglePixels(brush, rect); err != nil {
				return err
			}
		}
		// 第一行：内容
		r1 := walk.Rectangle{
			X:      pad, Y: y + walk.IntFrom96DPI(9, dpi),
			Width:  bounds.Width - pad - walk.IntFrom96DPI(16, dpi),
			Height: walk.IntFrom96DPI(20, dpi),
		}
		if err := canvas.DrawTextPixels(it.text, fl.fntText, colText, r1, walk.TextEndEllipsis|walk.TextSingleLine); err != nil {
			return err
		}
		// 第二行：相对时间
		r2 := walk.Rectangle{
			X:      pad, Y: y + walk.IntFrom96DPI(30, dpi),
			Width:  bounds.Width - pad - walk.IntFrom96DPI(16, dpi),
			Height: walk.IntFrom96DPI(16, dpi),
		}
		if err := canvas.DrawTextPixels(it.timeStr, fl.fntTime, colTextGray, r2, walk.TextEndEllipsis|walk.TextSingleLine); err != nil {
			return err
		}
		y += rowH
	}
	return nil
}
