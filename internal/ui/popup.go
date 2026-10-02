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
		return fmt.Sprintf("%d分钟前", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d小时前", int(d.Hours()))
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
		m.rows[i] = histRow{Text: clampText(e.Text, 48), TimeStr: relTime(e.ReceivedAt)}
	}
	m.PublishRowsReset()
}

// historyPopup 是 OneDrive 风格的历史弹窗（方案 §4.1）。
type historyPopup struct {
	tray     *Tray
	win      *walk.MainWindow
	tv       *walk.TableView
	model    *histModel
	snapshot win.FocusSnapshot
	shownAt  time.Time
}

func (t *Tray) ensureHistoryPopup() {
	if t.popup != nil {
		return
	}
	p := &historyPopup{tray: t, model: &histModel{}}
	var w *walk.MainWindow
	var tv *walk.TableView

	err := MainWindow{
		AssignTo: &w,
		Title:    "AirType 历史",
		Size:     Size{Width: 380, Height: 480},
		Layout:   VBox{MarginsZero: true},
		Children: []Widget{
			Composite{
				Layout: HBox{Margins: Margins{Left: 14, Top: 12, Right: 14, Bottom: 8}},
				Children: []Widget{
					Label{Text: "隔空打字", Font: Font{Family: "Segoe UI", PointSize: 14, Bold: true}},
					HSpacer{},
					Label{Text: "单击重发 · 右键复制/删除", TextColor: walk.RGB(120, 120, 120)},
				},
			},
			TableView{
				AssignTo:            &tv,
				Model:               p.model,
				ColumnsOrderable:    false,
				ColumnsSizable:      false,
				MultiSelection:      false,
				Columns: []TableViewColumn{
					{Title: "内容", Width: 260},
					{Title: "时间", Width: 78, Alignment: AlignFar},
				},
				OnMouseDown: p.onMouseDown,
				OnKeyDown: func(key walk.Key) {
					if key == walk.KeyEscape {
						p.hide()
					}
				},
				ContextMenuItems: []MenuItem{
					Action{Text: "复制", OnTriggered: p.copyCurrent},
					Action{Text: "删除", OnTriggered: p.deleteCurrent},
				},
			},
		},
	}.Create()
	if err != nil {
		t.cfg.Logger.Error("创建历史弹窗失败", "err", err)
		return
	}
	p.win = w
	p.tv = tv
	win.MakeTopmostToolWindow(uintptr(w.Handle()))
	// 双击 / 回车与单击等价：重发
	tv.ItemActivated().Attach(func() {
		if idx := tv.CurrentIndex(); idx >= 0 {
			p.reinject(idx)
		}
	})
	t.popup = p
}

// onMouseDown：左键单击条目 → 重发；右键 → 选中该行（供上下文菜单）。
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

// reinject 弹窗收起 → 焦点还原 → 注入；还原失败走剪贴板兜底（方案 §4.2）。
func (p *historyPopup) reinject(i int) {
	if i < 0 || i >= len(p.model.rows) {
		return
	}
	entries := p.tray.cfg.History.All()
	if i >= len(entries) {
		return
	}
	text := entries[i].Text
	snap := p.snapshot
	p.hide()

	go func() {
		if err := win.RestoreFocus(snap); err != nil {
			p.tray.cfg.Logger.Warn("焦点还原失败，走剪贴板兜底", "err", err)
			p.tray.copyToClipboard(text)
			p.tray.mw.Synchronize(func() {
				_ = p.tray.ni.ShowInfo("AirType", "原窗口不可用，文本已复制到剪贴板，请手动粘贴")
			})
			return
		}
		if err := p.tray.cfg.InjectText(text); err != nil {
			p.tray.cfg.Logger.Error("重注入失败", "err", err)
		}
	}()
}

func (p *historyPopup) copyCurrent() {
	idx := p.tv.CurrentIndex()
	if idx < 0 {
		return
	}
	entries := p.tray.cfg.History.All()
	if idx < len(entries) {
		p.tray.copyToClipboard(entries[idx].Text)
	}
}

func (p *historyPopup) deleteCurrent() {
	idx := p.tv.CurrentIndex()
	if idx < 0 {
		return
	}
	entries := p.tray.cfg.History.All()
	if idx < len(entries) {
		p.tray.cfg.History.Delete(entries[idx].ID)
	}
}

func (p *historyPopup) reload() {
	if p.tray.cfg.History == nil {
		return
	}
	p.model.refresh(p.tray.cfg.History.All())
}

// show 锚定到工作区右下角并显示，启动失焦自动关闭监听。
func (p *historyPopup) show() {
	ww, wh := win.WorkArea()
	w96, h96 := 380, 480
	// 以像素为基准右下角锚定（96dpi 尺寸随窗口 DPI 已由 walk 处理窗口尺寸）
	_ = p.win.SetBoundsPixels(walk.Rectangle{
		X:      int(ww) - w96 - 16,
		Y:      int(wh) - h96 - 12,
		Width:  w96,
		Height: h96,
	})
	p.reload()
	p.win.Show()
	p.shownAt = time.Now()
	go p.watchFocusLoss()
}

func (p *historyPopup) hide() {
	p.win.Hide()
}

// watchFocusLoss 弹窗失焦（点到别处）自动关闭；
// 上下文菜单（窗口类 #32768）持有前台时不视为失焦。
func (p *historyPopup) watchFocusLoss() {
	for range time.Tick(250 * time.Millisecond) {
		if !p.win.Visible() {
			return
		}
		// 刚 Show 的瞬间前台切换有延迟，给 500ms 宽限
		if time.Since(p.shownAt) < 500*time.Millisecond {
			continue
		}
		if uintptr(win.Foreground()) == uintptr(p.win.Handle()) {
			continue
		}
		if win.ForegroundClassName() == "#32768" {
			continue
		}
		p.tray.mw.Synchronize(func() { p.win.Hide() })
		return
	}
}
