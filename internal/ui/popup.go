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

// 配色（现代化浅色主题）
var (
	colWhite    = walk.RGB(255, 255, 255)
	colZebra    = walk.RGB(248, 249, 250)
	colTextGray = walk.RGB(138, 143, 150)
	colDotGreen = walk.RGB(34, 197, 94)
	colDotRed   = walk.RGB(239, 68, 68)
	colDotGray  = walk.RGB(156, 163, 175)
)

// histModel 是历史列表的表模型（walk.TableModel）。
type histModel struct {
	walk.TableModelBase
	rows []histRow
}

type histRow struct {
	Text    string
	TimeStr string
}

func (m *histModel) RowCount() int { return len(m.rows) }

func (m *histModel) Value(row, col int) interface{} {
	if row < 0 || row >= len(m.rows) {
		return ""
	}
	if col == 0 {
		return m.rows[row].Text
	}
	return m.rows[row].TimeStr
}

func (m *histModel) refresh(entries []history.Entry) {
	m.rows = make([]histRow, len(entries))
	for i, e := range entries {
		m.rows[i] = histRow{Text: clampText(e.Text, 42), TimeStr: relTime(e.ReceivedAt)}
	}
	m.PublishRowsReset()
}

// historyPopup 是 OneDrive 风格的历史弹窗（方案 §4.1）。
type historyPopup struct {
	tray      *Tray
	win       *walk.MainWindow
	tv        *walk.TableView
	model     *histModel
	snapshot  win.FocusSnapshot
	shownAt   time.Time
	statusLbl *walk.Label
}

func (t *Tray) ensureHistoryPopup() {
	if t.popup != nil {
		return
	}
	p := &historyPopup{tray: t, model: &histModel{}}
	var w *walk.MainWindow
	var tv *walk.TableView
	var status *walk.Label

	err := MainWindow{
		AssignTo:   &w,
		Title:      "AirType 历史",
		Size:       Size{Width: 400, Height: 520},
		Background: SolidColorBrush{Color: colWhite},
		Layout:     VBox{MarginsZero: true, SpacingZero: true},
		Children: []Widget{
			Composite{
				Background: SolidColorBrush{Color: colWhite},
				Layout:     HBox{Margins: Margins{Left: 18, Top: 16, Right: 18, Bottom: 10}},
				Children: []Widget{
					Label{
						Text:  "隔空打字",
						Font:  Font{Family: "Segoe UI", PointSize: 15, Bold: true},
					},
					Label{AssignTo: &status, Text: "●", TextColor: colDotGray},
					HSpacer{},
					Label{
						Text:      "单击重发 · 右键复制/删除",
						TextColor: colTextGray,
					},
				},
			},
			TableView{
				AssignTo:       &tv,
				Model:          p.model,
				Background:     SolidColorBrush{Color: colWhite},
				ColumnsOrderable:   false,
				ColumnsSizable:     false,
				MultiSelection:     false,
				Columns: []TableViewColumn{
					{Title: "内容", Width: 290},
					{Title: "时间", Width: 74, Alignment: AlignFar},
				},
				StyleCell: p.styleCell,
				OnMouseDown: p.onMouseDown,
				OnKeyDown: func(key walk.Key) {
					if key == walk.KeyEscape {
						p.hide()
					}
				},
				ContextMenuItems: []MenuItem{
					Action{Text: "重发（打回原输入框）", OnTriggered: p.reinjectCurrent},
					Action{Text: "复制", OnTriggered: p.copyCurrent},
					Action{Text: "删除", OnTriggered: p.deleteCurrent},
				},			},
		},
	}.Create()
	if err != nil {
		t.cfg.Logger.Error("创建历史弹窗失败", "err", err)
		return
	}
	p.win = w
	p.tv = tv
	p.statusLbl = status
	win.MakeTopmostToolWindow(uintptr(w.Handle()))
	// 双击 / 回车 = 重发（打回打开弹窗前的输入框）
	tv.ItemActivated().Attach(func() {
		p.reinjectCurrent()
	})
	t.popup = p
}

// styleCell 现代化样式：时间列灰字、奇数行浅灰底（斑马纹）。
func (p *historyPopup) styleCell(style *walk.CellStyle) {
	if style.Col() == 1 {
		style.TextColor = colTextGray
	}
	if style.Row()%2 == 1 {
		style.BackgroundColor = colZebra
	}
}

// onMouseDown：左键单击条目 → 重发（还原焦点后注入）；右键 → 选中该行。
func (p *historyPopup) onMouseDown(x, y int, button walk.MouseButton) {
	idx := p.tv.IndexAt(x, y)
	if idx < 0 {
		return
	}
	if button == walk.RightButton {
		_ = p.tv.SetCurrentIndex(idx)
		return
	}
	if button == walk.LeftButton {
		p.reinject(idx)
	}
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

// reinject 重发指定条目：还原焦点 → 收起弹窗 → 注入（方案 §4.2）。
func (p *historyPopup) reinject(i int) {
	if i < 0 || i >= len(p.model.rows) {
		return
	}
	entries := p.tray.cfg.History.All()
	if i >= len(entries) {
		return
	}
	p.injectWithRestore(entries[i].Text)
}

// injectWithRestore 焦点还原与注入（重发/弹窗期间新消息共用）。
//
// 顺序至关重要：必须趁弹窗仍持有前台（此时进程有前台激活权）先把目标
// 窗口切回前台，然后再收起弹窗——顺序颠倒的话，弹窗一隐藏本进程就
// 失去前台权限，SetForegroundWindow 会被系统的前台锁拒绝，这正是
// "点了没反应、回不到原窗口"的根因。
func (p *historyPopup) injectWithRestore(text string) {
	snap := p.snapshot

	go func() {
		if err := win.RestoreFocus(snap); err != nil {
			p.tray.cfg.Logger.Warn("焦点还原失败，走剪贴板兜底", "err", err)
			p.tray.mw.Synchronize(func() {
				p.hide()
				p.tray.copyToClipboard(text)
				_ = p.tray.ni.ShowInfo("AirType", "原窗口不可用，文本已复制到剪贴板，请手动粘贴")
			})
			return
		}
		// 目标窗口已在前台，此刻收起弹窗不会打扰它
		p.tray.mw.Synchronize(func() { p.hide() })
		// 等目标窗口内部焦点控件就绪再注入
		time.Sleep(60 * time.Millisecond)
		if err := p.tray.cfg.InjectText(text); err != nil {
			p.tray.cfg.Logger.Error("重注入失败", "err", err)
		}
	}()
}

func (p *historyPopup) copyCurrent() {
	p.copyAt(p.tv.CurrentIndex())
}

// reinjectCurrent 重发当前选中条目（右键菜单/双击/回车触发）。
func (p *historyPopup) reinjectCurrent() {
	p.reinject(p.tv.CurrentIndex())
}

func (p *historyPopup) deleteCurrent() {
	idx := p.tv.CurrentIndex()
	if idx < 0 {
		return
	}
	entries := p.tray.cfg.History.All()
	if idx < len(entries) {
		p.tray.cfg.History.Delete(entries[idx].ID)
		p.reload()
	}
}

func (p *historyPopup) reload() {
	if p.tray.cfg.History == nil {
		return
	}
	p.model.refresh(p.tray.cfg.History.All())
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

// show 显示弹窗：主动激活（防"显示但未激活"被误判失焦）、锚定到
// 鼠标所在显示器工作区右下角（点击托盘时鼠标就在图标附近）。
func (p *historyPopup) show() {
	p.reload()
	p.updateHeader(p.tray.state)
	p.win.Show()
	p.anchor()
	p.shownAt = time.Now()
	p.tray.popupVisible.Store(true)
	// 托盘点击赋予了前台激活权，这里显式激活弹窗；
	// 若不激活，弹窗会被失焦守卫当成"已失焦"而在宽限期后误关闭
	// ——这正是"时弹时不弹"的根因。
	go func() {
		_ = win.Activate(win.Hwnd(p.win.Handle()), 1200*time.Millisecond)
	}()
	go p.watchFocusLoss()
}

// anchor 用窗口"实际像素尺寸"（已含 DPI 缩放）做右下角锚定并钳制，
// 修复按 96-DPI 尺寸估算导致的溢出屏幕问题。
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

// watchFocusLoss 失焦自动关闭：
//  - 弹窗曾获得过前台 → 失去前台即关闭；
//  - 上下文菜单（#32768）持有前台时不算失焦；
//  - 前台尚未落到弹窗（激活未站稳/被系统回退）：只要前台仍是打开前的
//    那个窗口就保持显示，绝不按计时器强关——那是"时弹时不弹"的来源；
//    用户切到任何第三方窗口时才关闭。
func (p *historyPopup) watchFocusLoss() {
	popupHwnd := uintptr(p.win.Handle())
	preFg := uintptr(p.snapshot.Foreground) // 打开弹窗前的前台窗口
	wasActivated := false
	for range time.Tick(250 * time.Millisecond) {
		if !p.win.Visible() {
			return
		}
		fg := uintptr(win.Foreground())
		if fg == popupHwnd {
			wasActivated = true
			continue
		}
		if win.ForegroundClassName() == "#32768" {
			continue
		}
		if wasActivated || (fg != 0 && fg != preFg) {
			p.tray.mw.Synchronize(func() { p.hide() })
			return
		}
		// fg == preFg：激活被系统回退，弹窗保持可用（点击条目仍有效）
	}
}
