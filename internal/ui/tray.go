//go:build windows

// Package ui 实现托盘常驻界面：状态图标（绿/灰/红）、右键菜单、
// 扫码二维码弹窗、气泡通知。与业务层通过回调解耦（见方案 §4.3）。
package ui

import (
	"bytes"
	"fmt"
	"image/png"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
	"github.com/skip2/go-qrcode"

	"github.com/TeemoSun/AirType/internal/history"
	"github.com/TeemoSun/AirType/internal/win"
)

// TrayState 是托盘显示的连接状态（与 bot.State 解耦）。
type TrayState int

const (
	StateNeedQR TrayState = iota // 需要扫码
	StateConnected               // 已连接
	StateDisconnected            // 断开
)

func (s TrayState) String() string {
	switch s {
	case StateConnected:
		return "已连接"
	case StateNeedQR:
		return "待扫码"
	case StateDisconnected:
		return "已断开"
	default:
		return "未知"
	}
}

// Config 是 Tray 依赖的业务回调。
type Config struct {
	Logger *slog.Logger

	// History 为历史存储；为 nil 时左键单击与"清空历史"菜单不可用。
	History *history.Store
	// InjectText 把文本注入当前前台窗口（重发/新消息共用）。
	InjectText func(text string) error

	// TogglePause 切换暂停注入，返回切换后的暂停状态。
	TogglePause func() bool
	// Rescan 重新扫码（停 bot、删 token、重启）。
	Rescan func()
	// OpenLog 打开日志文件所在位置。
	OpenLog func()
	// Autostart 查询/设置开机自启（计划任务）。
	AutostartEnabled func() bool
	AutostartSet     func(bool) error
}

// Tray 持有托盘 UI。所有公开方法线程安全（内部 marshal 到 UI 线程）。
type Tray struct {
	cfg          Config
	mw           *walk.MainWindow
	ni           *walk.NotifyIcon
	popup        *historyPopup
	popupVisible atomic.Bool

	pauseAction  *walk.Action
	state        TrayState
	paused       bool
	lastReceived time.Time
	qrWin        *walk.MainWindow
	qrView       *walk.ImageView
	qrShown      bool
}

// NewTray 创建隐藏主窗口、托盘图标与右键菜单（不进入消息循环）。
func NewTray(cfg Config) (*Tray, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	t := &Tray{cfg: cfg, state: StateNeedQR}

	mw, err := walk.NewMainWindow()
	if err != nil {
		return nil, fmt.Errorf("ui: 创建主窗口失败: %w", err)
	}
	t.mw = mw

	ni, err := walk.NewNotifyIcon(mw)
	if err != nil {
		return nil, fmt.Errorf("ui: 创建托盘图标失败: %w", err)
	}
	t.ni = ni
	if err := t.applyIcon(); err != nil {
		return nil, err
	}
	_ = ni.SetToolTip("AirType · 启动中…")
	_ = ni.SetVisible(true)

	t.buildMenu()

	// 左键单击：待扫码时弹出二维码窗口；有历史时弹窗做开关切换；否则气泡摘要
	ni.MouseDown().Attach(func(x, y int, button walk.MouseButton) {
		if button != walk.LeftButton {
			return
		}
		if t.state == StateNeedQR {
			if t.qrWin != nil && t.qrShown {
				t.qrWin.Show()
			}
			return
		}
		if t.cfg.History != nil {
			// 再点一次托盘 = 收起弹窗（开关语义）
			if t.popup != nil && t.popup.win.Visible() {
				t.popup.hide()
				return
			}
			t.ShowHistory()
			return
		}
		_ = ni.ShowInfo("AirType", t.StatusText())
	})

	return t, nil
}

func (t *Tray) buildMenu() {
	menu := t.ni.ContextMenu()

	pause := walk.NewAction()
	pause.SetText("暂停注入")
	pause.Triggered().Attach(func() {
		if t.cfg.TogglePause == nil {
			return
		}
		t.SetPaused(t.cfg.TogglePause())
	})
	menu.Actions().Add(pause)
	t.pauseAction = pause

	rescan := walk.NewAction()
	rescan.SetText("重新扫码")
	rescan.Triggered().Attach(func() {
		if t.cfg.Rescan != nil {
			t.cfg.Rescan()
		}
	})
	menu.Actions().Add(rescan)

	openLog := walk.NewAction()
	openLog.SetText("打开日志")
	openLog.Triggered().Attach(func() {
		if t.cfg.OpenLog != nil {
			t.cfg.OpenLog()
		}
	})
	menu.Actions().Add(openLog)

	clearHist := walk.NewAction()
	clearHist.SetText("清空历史")
	clearHist.Triggered().Attach(func() {
		if t.cfg.History == nil {
			return
		}
		t.cfg.History.Clear()
		_ = t.ni.ShowInfo("AirType", "历史已清空")
	})
	menu.Actions().Add(clearHist)

	autostart := walk.NewAction()
	autostart.SetText("开机自启")
	autostart.SetCheckable(true)
	if t.cfg.AutostartEnabled != nil {
		autostart.SetChecked(t.cfg.AutostartEnabled())
	}
	autostart.Triggered().Attach(func() {
		if t.cfg.AutostartSet == nil {
			return
		}
		want := !autostart.Checked()
		if err := t.cfg.AutostartSet(want); err != nil {
			t.cfg.Logger.Error("设置开机自启失败", "want", want, "err", err)
			_ = t.ni.ShowError("AirType", "设置开机自启失败："+err.Error())
			return
		}
		autostart.SetChecked(want)
	})
	menu.Actions().Add(autostart)

	menu.Actions().Add(walk.NewSeparatorAction())

	quit := walk.NewAction()
	quit.SetText("退出")
	quit.Triggered().Attach(func() {
		_ = t.ni.SetVisible(false)
		_ = t.ni.Dispose()
		t.mw.Close()
	})
	menu.Actions().Add(quit)
}

// Run 进入消息循环，阻塞至托盘退出。
func (t *Tray) Run() {
	t.mw.Run()
}

// SetState 更新连接状态（线程安全）。
func (t *Tray) SetState(s TrayState) {
	t.mw.Synchronize(func() {
		t.state = s
		_ = t.applyIcon()
		_ = t.ni.SetToolTip(t.StatusText())
		if t.popup != nil {
			t.popup.updateHeader(s)
		}
	})
}

// SetPaused 更新暂停状态（线程安全）。
func (t *Tray) SetPaused(paused bool) {
	t.mw.Synchronize(func() {
		t.paused = paused
		if t.pauseAction != nil {
			if paused {
				t.pauseAction.SetText("恢复注入")
			} else {
				t.pauseAction.SetText("暂停注入")
			}
		}
		_ = t.applyIcon()
		_ = t.ni.SetToolTip(t.StatusText())
	})
}

// SetLastReceived 记录最后收信时间（线程安全），供 tooltip 自检展示。
func (t *Tray) SetLastReceived(ts time.Time) {
	t.mw.Synchronize(func() {
		t.lastReceived = ts
		_ = t.ni.SetToolTip(t.StatusText())
	})
}

// StatusText 汇总状态文案。
func (t *Tray) StatusText() string {
	txt := "AirType · " + t.state.String()
	if t.paused {
		txt = "AirType · 已暂停（只记录不注入）"
	}
	if !t.lastReceived.IsZero() {
		txt += " · 最后收信 " + t.lastReceived.Format("15:04:05")
	}
	return txt
}

// NotifyError 弹出错误气泡（线程安全）。
func (t *Tray) NotifyError(title, info string) {
	t.mw.Synchronize(func() {
		_ = t.ni.ShowError(title, info)
	})
}

// NotifyInfo 弹出提示气泡（线程安全）。
func (t *Tray) NotifyInfo(title, info string) {
	t.mw.Synchronize(func() {
		_ = t.ni.ShowInfo(title, info)
	})
}

// ShowHistory 打开历史弹窗：先捕获当前焦点快照（方案 §4.2），再显示弹窗。
func (t *Tray) ShowHistory() {
	t.mw.Synchronize(func() {
		if t.cfg.History == nil {
			return
		}
		t.ensureHistoryPopup()
		if t.popup == nil {
			return
		}
		t.popup.snapshot = win.CaptureFocus()
		t.popup.show()
	})
}

// HistoryChanged 历史变更通知（弹窗打开时刷新列表）。线程安全。
func (t *Tray) HistoryChanged() {
	t.mw.Synchronize(func() {
		if t.popup != nil && t.popup.win.Visible() {
			t.popup.reload()
		}
	})
}

// PopupVisible 历史弹窗是否处于打开状态（线程安全，供消息处理分支：
// 弹窗打开时注入会打进弹窗自身，跳过改为仅入历史）。
func (t *Tray) PopupVisible() bool {
	return t.popupVisible.Load()
}

// HidePopupForTest 收起历史弹窗（供自动化测试模拟点击 ✕）。线程安全。
func (t *Tray) HidePopupForTest() {
	t.mw.Synchronize(func() {
		if t.popup != nil && t.popup.win.Visible() {
			t.popup.hide()
		}
	})
}

// PopupIsShownForTest 返回弹窗窗口当前是否可见（供测试检测闪关）。
func (t *Tray) PopupIsShownForTest() bool {
	done := make(chan bool, 1)
	t.mw.Synchronize(func() {
		done <- t.popup != nil && t.popup.win.Visible()
	})
	select {
	case v := <-done:
		return v
	case <-time.After(2 * time.Second):
		return false
	}
}

// PopupCopyForTest 复制弹窗第 i 条（模拟单击条目），供剪贴板回读验证。线程安全。
func (t *Tray) PopupCopyForTest(i int) {
	t.mw.Synchronize(func() {
		if t.popup == nil || t.popup.list == nil {
			return
		}
		items := t.popup.list.items
		if i < 0 || i >= len(items) {
			return
		}
		t.popup.copyAt(items[i].id)
	})
}

// ShowQR 显示扫码二维码窗口（线程安全）。imageURL 为二维码内容链接。
func (t *Tray) ShowQR(imageURL string) {
	pngBytes, err := qrcode.Encode(imageURL, qrcode.Medium, 420)
	if err != nil {
		t.cfg.Logger.Error("生成二维码失败", "err", err)
		return
	}
	t.mw.Synchronize(func() {
		t.ensureQRWindow()
		if t.qrWin == nil {
			return
		}
		// 先 Show 让窗口落到具体显示器上，DPI() 才是真实值
		if !t.qrWin.Visible() {
			t.qrWin.Show()
		}
		img, err := png.Decode(bytes.NewReader(pngBytes))
		if err != nil {
			t.cfg.Logger.Error("解码二维码失败", "err", err)
			return
		}
		dpi := t.qrWin.DPI()
		bmp, err := walk.NewBitmapFromImageForDPI(img, dpi)
		if err != nil {
			t.cfg.Logger.Error("创建二维码位图失败", "err", err)
			return
		}
		t.qrView.SetImage(bmp)
		t.qrShown = true
	})
}

// HideQR 关闭二维码窗口（登录成功后调用，线程安全）。
func (t *Tray) HideQR() {
	t.mw.Synchronize(func() {
		if t.qrWin != nil {
			t.qrWin.Hide()
		}
	})
}

// ensureQRWindow 惰性创建二维码窗口（必须在 UI 线程调用）。
// 注意：walk 窗口带子控件必须设 Layout，否则 WM_SIZE 时 startLayout 崩溃。
func (t *Tray) ensureQRWindow() {
	if t.qrWin != nil {
		return
	}
	var w *walk.MainWindow
	var iv *walk.ImageView
	err := MainWindow{
		AssignTo:   &w,
		Title:      "AirType · 微信扫码绑定",
		Size:       Size{Width: 480, Height: 580},
		Background: SolidColorBrush{Color: walk.RGB(255, 255, 255)},
		Layout:     VBox{Margins: Margins{Left: 20, Top: 24, Right: 20, Bottom: 20}, Spacing: 14},
		Children: []Widget{
			Label{
				Text: "扫码绑定微信",
				Font: Font{Family: "Segoe UI", PointSize: 15, Bold: true},
			},
			Label{
				Text:      "用手机微信扫描下方二维码，授权后电脑端自动连接",
				TextColor: walk.RGB(138, 143, 150),
			},
			ImageView{AssignTo: &iv, MinSize: Size{Width: 420, Height: 420}},
			VSpacer{},
		},
	}.Create()
	if err != nil {
		t.cfg.Logger.Error("创建二维码窗口失败", "err", err)
		return
	}
	t.qrWin = w
	t.qrView = iv
	win.RoundCorners(uintptr(w.Handle()))
}

// applyIcon 按状态换图标（须在 UI 线程调用）。
func (t *Tray) applyIcon() error {
	name := "red"
	switch {
	case t.paused:
		name = "gray"
	case t.state == StateConnected:
		name = "green"
	}
	icon, err := stateIcon(name)
	if err != nil {
		return fmt.Errorf("ui: 加载图标 %s 失败: %w", name, err)
	}
	defer icon.Dispose()
	return t.ni.SetIcon(icon)
}
