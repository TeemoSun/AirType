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

// clampText 折叠换行为空格并截断到 maxRunes（不追加省略号：
// 绘制时的 TextEndEllipsis 负责尾省略，避免双重省略号）。
func clampText(s string, maxRunes int) string {
	out := make([]rune, 0, maxRunes)
	n := 0
	for _, r := range s {
		if r == '\n' || r == '\r' {
			r = ' '
		}
		if n == maxRunes {
			break
		}
		out = append(out, r)
		n++
	}
	return string(out)
}

// 状态点颜色（与托盘图标语义一致：绿=正常，黄=需要注意，红=出错，灰=未绑定）。
var (
	colDotGreen  = walk.RGB(34, 197, 94)
	colDotRed    = walk.RGB(239, 68, 68)
	colDotYellow = walk.RGB(245, 158, 11)
	colDotGray   = walk.RGB(156, 163, 175)
)

// 布局常量（96dpi 基准）。
const (
	flRowH96       = 54 // 行高
	flTextPad96    = 18
	flMaxTextRunes = 36
	flCopyFlash    = 1200 * time.Millisecond // "已复制"行内反馈时长
	flMsgFlash     = 2200 * time.Millisecond // "新消息已入历史"头部提示时长
	flRelTimeTick  = 30 * time.Second        // 相对时间刷新间隔
)

// historyPopup 是 OneDrive 风格的无边框圆角历史浮层（方案 §4.1）。
type historyPopup struct {
	tray      *Tray
	win       *walk.MainWindow
	list      *fluentList
	snapshot  win.FocusSnapshot
	statusLbl *walk.Label // 状态点 ●
	statusTxt *walk.Label // 状态文字（含"已暂停/新消息"闪现）
	closeBtn  *walk.CustomWidget

	flashMsg     string
	flashUntil   time.Time
	ticker       *time.Ticker // 相对时间定期刷新
	watchRunning bool         // 失焦监视单实例守卫
}

func (t *Tray) ensureHistoryPopup() {
	_, dark := currentPalette()
	if t.popup != nil {
		if t.popupDark == dark {
			return
		}
		t.popup.win.Dispose()
		t.popup = nil
	}
	pal, _ := currentPalette()

	p := &historyPopup{tray: t}
	var w *walk.MainWindow
	var status, statusText *walk.Label
	var closeBtn *walk.CustomWidget
	var listHost *walk.Composite

	err := MainWindow{
		AssignTo:   &w,
		Title:      "AirType 历史",
		Size:       Size{Width: 400, Height: 520},
		Background: SolidColorBrush{Color: pal.Window},
		Layout:     VBox{MarginsZero: true, SpacingZero: true},
		OnKeyDown: func(key walk.Key) {
			if key == walk.KeyEscape {
				p.hide()
			}
		},
		Children: []Widget{
			// 标题行：产品名 + 状态点 + 状态文字 + 关闭按钮
			Composite{
				Background: SolidColorBrush{Color: pal.Window},
				Layout:     HBox{Margins: Margins{Left: 20, Top: 14, Right: 10, Bottom: 8}, Spacing: 6},
				Children: []Widget{
					Label{Text: "AirType", Font: Font{Family: "Segoe UI", PointSize: 14, Bold: true}, TextColor: pal.Text},
					Label{AssignTo: &status, Text: "●", TextColor: colDotGray},
					Label{AssignTo: &statusText, Text: "未绑定", TextColor: pal.TextSecondary},
					HSpacer{},
					CustomWidget{
						AssignTo: &closeBtn,
						Paint:    func(canvas *walk.Canvas, bounds walk.Rectangle) error { return paintCloseButton(canvas, bounds, pal) },
						MinSize:  Size{Width: 34, Height: 28},
						MaxSize:  Size{Width: 34, Height: 28},
					},
				},
			},
			// 列表宿主：自绘 Fluent 列表挂到这里；StretchFactor=1 占满
			// 头部/底部之外的剩余高度（否则多余空间被摊进头部，比例怪异）
			Composite{
				AssignTo:      &listHost,
				Background:    SolidColorBrush{Color: pal.Window},
				Layout:        VBox{MarginsZero: true, SpacingZero: true},
				StretchFactor: 1,
			},
			// 底部提示
			Composite{
				Background: SolidColorBrush{Color: pal.Window},
				Layout:     HBox{Margins: Margins{Left: 20, Top: 8, Right: 20, Bottom: 12}},
				Children: []Widget{
					Label{Text: "单击复制 · 右键复制/删除/重新打字", TextColor: pal.TextSecondary},
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
	p.statusTxt = statusText

	// 无边框 + 圆角（Win11 风格浮层）
	win.MakeTopmostToolWindow(uintptr(w.Handle()))
	win.MakeBorderlessRoundedPopup(uintptr(w.Handle()))

	fl, err := newFluentList(listHost, p)
	if err != nil {
		t.cfg.Logger.Error("创建列表失败", "err", err)
		return
	}
	p.list = fl
	p.closeBtn = closeBtn
	closeBtn.MouseDown().Attach(func(x, y int, button walk.MouseButton) {
		if button == walk.LeftButton {
			p.hide()
		}
	})

	t.popup = p
	t.popupDark = dark
}

// statusText 计算头部状态文字（闪现消息 > 暂停 > 通道状态）。
func (p *historyPopup) statusText() string {
	if time.Now().Before(p.flashUntil) {
		return p.flashMsg
	}
	if p.tray.paused {
		return "已暂停"
	}
	return p.tray.state.String()
}

// dotColor 状态点颜色，与托盘图标同语义。
func (p *historyPopup) dotColor() walk.Color {
	if p.tray.paused {
		return colDotYellow
	}
	switch p.tray.state {
	case StateConnected:
		return colDotGreen
	case StateError:
		return colDotRed
	case StateNeedQR, StateWarning, StateDisconnected:
		return colDotYellow
	default:
		return colDotGray
	}
}

// updateHeader 刷新状态点与文字（须在 UI 线程调用）。
func (p *historyPopup) updateHeader() {
	if p.statusLbl == nil {
		return
	}
	p.statusLbl.SetTextColor(p.dotColor())
	p.statusTxt.SetText(p.statusText())
}

// flash 在头部短暂显示一条提示（如"新消息已入历史"），到时自动恢复。
func (p *historyPopup) flash(msg string) {
	p.flashMsg = msg
	p.flashUntil = time.Now().Add(flMsgFlash)
	p.updateHeader()
	time.AfterFunc(flMsgFlash+200*time.Millisecond, func() {
		p.tray.mw.Synchronize(func() {
			if time.Now().After(p.flashUntil) {
				p.updateHeader()
			}
		})
	})
}

// show 显示弹窗：主动激活（防被失焦守卫误杀）、锚定鼠标所在显示器工作区右下角。
func (p *historyPopup) show() {
	p.reload()
	p.updateHeader()
	p.win.Show()
	p.anchor()
	if p.list != nil {
		p.list.Focus()
	}
	p.tray.popupVisible.Store(true)
	// 相对时间定期刷新（"刚刚"不该永远停在打开那一刻）
	p.ticker = time.NewTicker(flRelTimeTick)
	go func() {
		for range p.ticker.C {
			p.tray.mw.Synchronize(func() {
				if !p.win.Visible() {
					return
				}
				p.refreshTimes()
			})
		}
	}()
	go func() {
		_ = win.Activate(win.Hwnd(p.win.Handle()), 1200*time.Millisecond)
	}()
	p.startFocusWatch()
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
	if p.ticker != nil {
		p.ticker.Stop()
		p.ticker = nil
	}
	p.tray.popupVisible.Store(false)
	p.win.Hide()
}

func (p *historyPopup) reload() {
	if p.tray.cfg.History == nil || p.list == nil {
		return
	}
	entries := p.tray.cfg.History.All()
	p.list.setItems(entries)
	p.refreshTimes()
}

// refreshTimes 重算相对时间并重绘（须在 UI 线程调用）。
func (p *historyPopup) refreshTimes() {
	if p.list == nil {
		return
	}
	for i := range p.list.items {
		p.list.items[i].timeStr = relTime(p.list.items[i].at)
	}
	p.list.w.Invalidate()
}

// copyAt 按条目 ID 复制文本到剪贴板（列表索引可能因新消息插入而漂移，
// 必须用 ID 定位）。剪贴板重试在后台 goroutine 进行（锁常被输入法/剪贴板
// 工具短暂占用，最多 10×50ms，不能卡 UI 线程）；成功只做行内"已复制"
// 反馈，不再弹气泡（高频操作+气泡正好挡在弹窗头顶），失败才提示。
func (p *historyPopup) copyAt(id int64) {
	var text string
	found := false
	for _, e := range p.tray.cfg.History.All() {
		if e.ID == id {
			text, found = e.Text, true
			break
		}
	}
	if !found {
		p.tray.cfg.Logger.Warn("复制目标不存在", "id", id)
		return
	}
	idc := id
	go func() {
		var err error
		for i := 0; i < 10; i++ {
			if err = walk.Clipboard().SetText(text); err == nil {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		p.tray.mw.Synchronize(func() {
			if err != nil {
				p.tray.cfg.Logger.Error("复制失败", "err", err)
				_ = p.tray.ni.ShowError("AirType", "复制失败；详情见日志")
				return
			}
			p.tray.cfg.Logger.Info("已复制", "chars", len([]rune(text)))
			if p.list != nil {
				p.list.markCopied(idc)
			}
		})
	}()
}

// retype 关闭弹窗并恢复焦点后"重新打字"（走业务注入链路）。
func (p *historyPopup) retype(id int64) {
	var text string
	found := false
	for _, e := range p.tray.cfg.History.All() {
		if e.ID == id {
			text, found = e.Text, true
			break
		}
	}
	if !found || p.tray.cfg.InjectText == nil {
		return
	}
	fg := uintptr(p.snapshot.Foreground)
	p.hide()
	go func() {
		if fg != 0 && win.IsWindowAlive(fg) {
			_ = win.Activate(win.Hwnd(fg), 1500*time.Millisecond) // 回到打开弹窗前的窗口
		}
		if err := p.tray.cfg.InjectText(text); err != nil {
			p.tray.cfg.Logger.Error("重新打字失败", "err", err)
		}
	}()
}

// startFocusWatch 失焦自动关闭（带回退容错，单实例守卫）：
//   - 弹窗曾获得前台 → 失去前台即关闭候选；但若前台回到打开前的窗口且
//     距激活不足 1.2s，视为系统前台权限瞬时回退（托盘点击的激活权很短），
//     不关闭——否则弹窗闪关，表现为"打不开"；
//   - 切到任何第三方窗口 → 立即关闭；
//   - 上下文菜单（#32768）持有前台时不算失焦。
func (p *historyPopup) startFocusWatch() {
	if p.watchRunning {
		return
	}
	p.watchRunning = true
	popupHwnd := uintptr(p.win.Handle())
	preFg := uintptr(p.snapshot.Foreground)
	wasActivated := false
	var activatedAt time.Time
	go func() {
		defer func() { p.watchRunning = false }()
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for range ticker.C {
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
	}()
}

// fluentItem 是列表条目。
type fluentItem struct {
	id      int64
	text    string // 截断后的显示文本
	full    string // 完整原文（tooltip 用）
	timeStr string
	at      time.Time
}

// fluentList 是自绘的 Fluent 风格列表：无网格线，悬停/选中高亮，
// 每条两行（内容 + 相对时间/已复制），滚轮翻页，右侧迷你滚动条，
// 左键复制、右键菜单（复制/删除/重新打字），键盘 ↑↓ 导航。
type fluentList struct {
	w        *walk.CustomWidget
	p        *historyPopup
	pal      Palette
	dwrite   *win.TextRenderer // 彩色 emoji 渲染；不可用时回退 GDI
	items    []fluentItem
	hover    int // -1 无
	sel      int // 键盘选中索引，-1 无
	scroll   int // 第一条可见条目的索引
	visRows  int // 最近一次绘制得到的可见行数
	copiedID int64
	copiedAt time.Time

	fntText *walk.Font
	fntTime *walk.Font
	brHover *walk.SolidColorBrush
	brSel   *walk.SolidColorBrush
	brThumb *walk.SolidColorBrush
	brBkgnd *walk.SolidColorBrush
}

func newFluentList(parent walk.Container, p *historyPopup) (*fluentList, error) {
	pal, _ := currentPalette()
	fl := &fluentList{p: p, pal: pal, hover: -1, sel: -1, copiedID: -1}

	var err error
	if fl.fntText, err = walk.NewFont("Segoe UI", 9, 0); err != nil {
		return nil, err
	}
	if fl.fntTime, err = walk.NewFont("Segoe UI", 8, 0); err != nil {
		return nil, err
	}
	if fl.brHover, err = walk.NewSolidColorBrush(pal.Hover); err != nil {
		return nil, err
	}
	if fl.brSel, err = walk.NewSolidColorBrush(pal.Selection); err != nil {
		return nil, err
	}
	if fl.brThumb, err = walk.NewSolidColorBrush(pal.ScrollThumb); err != nil {
		return nil, err
	}
	if fl.brBkgnd, err = walk.NewSolidColorBrush(pal.Window); err != nil {
		return nil, err
	}

	w, err := walk.NewCustomWidgetPixels(parent, 0, fl.paint)
	if err != nil {
		return nil, err
	}
	fl.w = w
	// 自绘控件默认白底擦除：深色主题下必须铺主题底色，
	// 否则整个列表区域是一块刺眼的白板（白字也看不见）
	w.SetBackground(fl.brBkgnd)

	w.MouseMove().Attach(fl.onMouseMove)
	w.MouseDown().Attach(fl.onMouseDown)
	w.MouseWheel().Attach(fl.onMouseWheel)
	w.KeyDown().Attach(fl.onKeyDown)

	// DirectWrite 渲染器：彩色 emoji + 更细腻的抗锯齿；初始化失败静默回退 GDI
	if r, err := win.GetTextRenderer(w.DPI()); err == nil {
		fl.dwrite = r
	} else {
		p.tray.cfg.Logger.Warn("DirectWrite 不可用，消息行使用 GDI 渲染（emoji 为黑白轮廓）", "err", err)
	}
	return fl, nil
}

// drawRowText 画一行文本：优先 DirectWrite（彩色 emoji/省略号截断），
// 失败回退 GDI DrawText。
func (fl *fluentList) drawRowText(canvas *walk.Canvas, text string, r walk.Rectangle, color walk.Color, big bool) error {
	if fl.dwrite != nil {
		if err := fl.dwrite.DrawText(uintptr(canvas.HDC()), text,
			int32(r.X), int32(r.Y), int32(r.Width), int32(r.Height), uint32(color), big); err == nil {
			return nil
		}
		// DWrite 本次失败：该行回退 GDI
	}
	f := fl.fntTime
	if big {
		f = fl.fntText
	}
	return canvas.DrawTextPixels(text, f, color, r, walk.TextEndEllipsis|walk.TextSingleLine)
}

// Focus 让列表获得键盘焦点（滚轮/按键事件发给焦点窗口）。
func (fl *fluentList) Focus() {
	_ = fl.w.SetFocus()
}

// setItems 更新数据并重绘。
func (fl *fluentList) setItems(entries []history.Entry) {
	fl.items = make([]fluentItem, len(entries))
	for i, e := range entries {
		fl.items[i] = fluentItem{
			id:   e.ID,
			text: clampText(e.Text, flMaxTextRunes),
			full: e.Text,
			at:   e.ReceivedAt,
		}
	}
	fl.clampScroll()
	fl.hover, fl.sel = -1, -1
	fl.w.Invalidate()
}

// markCopied 行内"已复制 ✓"反馈：时间行临时替换，1.2s 后还原。
func (fl *fluentList) markCopied(id int64) {
	fl.copiedID, fl.copiedAt = id, time.Now()
	fl.w.Invalidate()
	time.AfterFunc(flCopyFlash+150*time.Millisecond, func() {
		fl.p.tray.mw.Synchronize(func() {
			if time.Since(fl.copiedAt) >= flCopyFlash {
				fl.w.Invalidate()
			}
		})
	})
}

func (fl *fluentList) rowHPixels() int {
	return walk.IntFrom96DPI(flRowH96, fl.w.DPI())
}

func (fl *fluentList) clampScroll() {
	if fl.visRows > 0 {
		if max := len(fl.items) - fl.visRows; fl.scroll > max {
			fl.scroll = max
		}
	}
	if fl.scroll > len(fl.items)-1 {
		fl.scroll = len(fl.items) - 1
	}
	if fl.scroll < 0 {
		fl.scroll = 0
	}
}

// ensureVisible 让键盘选中行滚入可视区。
func (fl *fluentList) ensureVisible(idx int) {
	if fl.visRows <= 0 {
		return
	}
	if idx < fl.scroll {
		fl.scroll = idx
	}
	if idx >= fl.scroll+fl.visRows {
		fl.scroll = idx - fl.visRows + 1
	}
	fl.clampScroll()
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
		// 悬停显示完整原文：截断的条目不用盲复制
		if idx >= 0 && idx < len(fl.items) {
			fl.w.SetToolTipText(fl.items[idx].full)
		} else {
			fl.w.SetToolTipText("")
		}
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

// onKeyDown 键盘导航：↑↓ 选中、Enter 复制、Delete 删除、ESC 关闭。
func (fl *fluentList) onKeyDown(key walk.Key) {
	switch key {
	case walk.KeyUp, walk.KeyDown:
		if len(fl.items) == 0 {
			return
		}
		d := 1
		if key == walk.KeyUp {
			d = -1
		}
		if fl.sel < 0 {
			fl.sel = fl.scroll
		} else {
			fl.sel += d
		}
		if fl.sel < 0 {
			fl.sel = 0
		}
		if fl.sel >= len(fl.items) {
			fl.sel = len(fl.items) - 1
		}
		fl.ensureVisible(fl.sel)
		fl.w.Invalidate()
	case walk.KeyReturn:
		if fl.sel >= 0 && fl.sel < len(fl.items) {
			fl.p.copyAt(fl.items[fl.sel].id)
		}
	case walk.KeyDelete:
		if fl.sel >= 0 && fl.sel < len(fl.items) && fl.p.tray.cfg.History != nil {
			fl.p.tray.cfg.History.Delete(fl.items[fl.sel].id)
			fl.sel = -1
			fl.p.reload()
		}
	case walk.KeyEscape:
		fl.p.hide()
	}
}

func (fl *fluentList) onMouseDown(x, y int, button walk.MouseButton) {
	idx := fl.hitTest(x, y)
	if idx < 0 || idx >= len(fl.items) {
		return
	}
	id := fl.items[idx].id
	if button == walk.LeftButton {
		fl.sel = idx
		fl.p.copyAt(id)
		return
	}
	if button == walk.RightButton {
		choice := win.ShowContextMenu(uintptr(fl.p.win.Handle()), []string{"复制", "删除", "重新打字"})
		switch choice {
		case 0:
			fl.p.copyAt(id)
		case 1:
			if fl.p.tray.cfg.History != nil {
				fl.p.tray.cfg.History.Delete(id)
				fl.p.reload()
			}
		case 2:
			fl.p.retype(id)
		}
	}
}

// paint 自绘列表内容（坐标均为像素）。
func (fl *fluentList) paint(canvas *walk.Canvas, bounds walk.Rectangle) error {
	pal := fl.pal
	dpi := fl.w.DPI()
	rowH := fl.rowHPixels()
	pad := walk.IntFrom96DPI(flTextPad96, dpi)

	// 整面铺主题底色：与 SetBackground 双保险，深色主题绝不露白底
	if fl.brBkgnd != nil {
		if err := canvas.FillRectanglePixels(fl.brBkgnd, bounds); err != nil {
			return err
		}
	}

	if len(fl.items) == 0 {
		txt := fl.emptyText()
		r := walk.Rectangle{X: 0, Y: bounds.Height/2 - 20, Width: bounds.Width, Height: 40}
		return canvas.DrawTextPixels(txt, fl.fntText, pal.TextSecondary, r,
			walk.TextCenter|walk.TextVCenter|walk.TextSingleLine)
	}

	// 可见行数（用于钳制滚动与滚动条）
	fl.visRows = bounds.Height / rowH
	fl.clampScroll()

	y := 0
	for i := fl.scroll; i < len(fl.items) && y < bounds.Height; i++ {
		it := fl.items[i]
		// 行背景：键盘选中 > 悬停 > 无（去掉斑马纹：Fluent 列表靠悬停不靠条纹）
		var brush walk.Brush
		if i == fl.sel {
			brush = fl.brSel
		} else if i == fl.hover {
			brush = fl.brHover
		}
		if brush != nil {
			rect := walk.Rectangle{X: 6, Y: y + 2, Width: bounds.Width - 12, Height: rowH - 4}
			if err := canvas.FillRectanglePixels(brush, rect); err != nil {
				return err
			}
		}
		// 第一行：内容
		r1 := walk.Rectangle{
			X: pad, Y: y + walk.IntFrom96DPI(9, dpi),
			Width:  bounds.Width - pad - walk.IntFrom96DPI(16, dpi),
			Height: walk.IntFrom96DPI(20, dpi),
		}
		if err := fl.drawRowText(canvas, it.text, r1, pal.Text, true); err != nil {
			return err
		}
		// 第二行：相对时间；刚复制的行临时显示"已复制 ✓"
		line2, col := it.timeStr, pal.TextSecondary
		if it.id == fl.copiedID && time.Since(fl.copiedAt) < flCopyFlash {
			line2, col = "已复制 ✓", pal.Flash
		}
		r2 := walk.Rectangle{
			X: pad, Y: y + walk.IntFrom96DPI(31, dpi),
			Width:  bounds.Width - pad - walk.IntFrom96DPI(16, dpi),
			Height: walk.IntFrom96DPI(16, dpi),
		}
		if err := fl.drawRowText(canvas, line2, r2, col, false); err != nil {
			return err
		}
		y += rowH
	}

	// 迷你滚动条：内容溢出时在右缘显示 4px 圆头滑块
	if total := len(fl.items); total > fl.visRows && fl.visRows > 0 {
		thumbH := bounds.Height * fl.visRows / total
		if thumbH < 24 {
			thumbH = 24
		}
		maxScroll := total - fl.visRows
		thumbY := 0
		if maxScroll > 0 {
			thumbY = (bounds.Height - thumbH) * fl.scroll / maxScroll
		}
		rect := walk.Rectangle{X: bounds.Width - 6, Y: thumbY, Width: 4, Height: thumbH}
		if err := canvas.FillRectanglePixels(fl.brThumb, rect); err != nil {
			return err
		}
	}
	return nil
}

// emptyText 空态文案按当前通道生成。
func (fl *fluentList) emptyText() string {
	switch fl.p.tray.mode {
	case ChannelQQ:
		return "暂无消息，用手机 QQ 发一条试试"
	case ChannelWeChat:
		return "暂无消息，用手机微信发一条试试"
	default:
		return "暂无消息，绑定通道后用手机发一条试试"
	}
}

// paintCloseButton 自绘右上角 ✕（无边框窗口没有系统关闭键）。
// 先铺主题底色：自绘控件默认白底擦除，深色主题下会留一条白带。
func paintCloseButton(canvas *walk.Canvas, b walk.Rectangle, pal Palette) error {
	bg, err := walk.NewSolidColorBrush(pal.Window)
	if err == nil {
		defer bg.Dispose()
		if err := canvas.FillRectanglePixels(bg, b); err != nil {
			return err
		}
	}
	pen, err := walk.NewCosmeticPen(walk.PenSolid, pal.TextSecondary)
	if err != nil {
		return err
	}
	defer pen.Dispose()
	cx, cy := b.X+b.Width/2, b.Y+b.Height/2
	d := 4
	if err := canvas.DrawLinePixels(pen,
		walk.Point{X: cx - d, Y: cy - d}, walk.Point{X: cx + d, Y: cy + d}); err != nil {
		return err
	}
	return canvas.DrawLinePixels(pen,
		walk.Point{X: cx - d, Y: cy + d}, walk.Point{X: cx + d, Y: cy - d})
}
