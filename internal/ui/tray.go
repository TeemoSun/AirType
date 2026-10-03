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
	StateNeedQR       TrayState = iota // 未登录/需重新扫码（红）
	StateConnected                     // 一切正常（绿）
	StateWarning                       // 连接异常/长时间未收到消息（黄）
	StateDisconnected                  // 连接断开（黄）
)

func (s TrayState) String() string {
	switch s {
	case StateConnected:
		return "已连接"
	case StateWarning:
		return "连接异常"
	case StateNeedQR:
		return "待扫码"
	case StateDisconnected:
		return "已断开"
	default:
		return "未知"
	}
}

// ChannelMode 是当前绑定的消息通道（互斥：同时只能有一个）。
type ChannelMode int

const (
	ChannelNone   ChannelMode = iota // 未绑定（弹选择窗口任选微信或 QQ）
	ChannelWeChat                    // 微信通道
	ChannelQQ                        // QQ 通道
)

// ChannelChoice 是通道选择窗口的点击结果。
type ChannelChoice int

const (
	ChoiceWeChat ChannelChoice = iota
	ChoiceQQ
)

func (c ChannelChoice) String() string {
	if c == ChoiceQQ {
		return "QQ"
	}
	return "WeChat"
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
	// AutoEnterEnabled 查询"自动回车"初始状态；ToggleAutoEnter 切换并返回切换后的状态。
	AutoEnterEnabled func() bool
	ToggleAutoEnter  func() bool
	// ChooseChannel 通道选择窗口点击"微信"或"QQ"时回调（启动对应绑定流程）。
	ChooseChannel func(choice ChannelChoice)
	// QRDismissed 用户主动关掉扫码窗口（✕）时回调；QQ 绑定流程据此取消，
	// 微信扫码可安全忽略（SDK 继续等待，左键托盘可重新打开二维码）。
	QRDismissed func()
	// Logout 退出登录（清理当前通道凭据，回到未绑定状态，重新弹选择窗口）。
	Logout func()
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
	autoEnter    *walk.Action
	logoutAction *walk.Action
	mode         ChannelMode
	state        TrayState
	paused       bool
	lastReceived time.Time
	chooserWin   *walk.MainWindow
	qrWin        *walk.MainWindow
	qrView       *walk.ImageView
	qrTitleLbl   *walk.Label
	qrHintLbl    *walk.Label
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
	t.attachTrayClick()

	// 托盘守护：walk 在 TaskbarCreated 后只以隐藏态重挂图标（实际仍不可见），
	// 且没有 NIM_ADD 级的公开恢复接口，图标一旦从托盘丢失便找不回来。
	// 这里子类化主窗口：任务栏重建或二次实例唤醒广播时，销毁重建图标。
	if err := win.SubclassTrayGuard(uintptr(t.mw.Handle()), t.onTaskbarRecreated, t.onWakeFromSecondInstance); err != nil {
		t.cfg.Logger.Warn("安装托盘守护失败（不影响其余功能）", "err", err)
	}

	return t, nil
}

// attachTrayClick 挂接托盘左键单击行为（图标重建后需对新图标重挂）。
func (t *Tray) attachTrayClick() {
	// 左键单击：未绑定时弹通道选择窗口；待扫码时弹二维码窗口；
	// 有历史时弹窗做开关切换；否则气泡摘要
	t.ni.MouseDown().Attach(func(x, y int, button walk.MouseButton) {
		if button != walk.LeftButton {
			return
		}
		if t.mode == ChannelNone {
			t.ShowChannelChooser()
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
		_ = t.ni.ShowInfo("AirType", t.StatusText())
	})
}

// onTaskbarRecreated 处理任务栏重建（explorer 重启等）：walk 已把自己那份
// 隐藏态图标挂回（发生在原窗口过程里），这里销毁重建为正常可见图标。
func (t *Tray) onTaskbarRecreated() {
	t.recreateNotifyIcon()
	t.cfg.Logger.Info("检测到任务栏重建，托盘图标已重建")
}

// onWakeFromSecondInstance 处理二次实例的唤醒广播：重建图标并明确告知用户。
func (t *Tray) onWakeFromSecondInstance() {
	t.recreateNotifyIcon()
	_ = t.ni.ShowInfo("AirType", "AirType 已在运行；托盘图标已重新显示（本次启动已忽略）")
	t.cfg.Logger.Info("二次实例唤醒：托盘图标已重建")
}

// recreateNotifyIcon 销毁并重建托盘图标。walk 的 SetVisible 走 NIM_MODIFY，
// 救不回已从托盘丢失的图标；新建 NotifyIcon 走 NIM_ADD 才是真正的重挂，
// 随后重建菜单、图标与点击处理。必须在 UI 线程调用。
func (t *Tray) recreateNotifyIcon() {
	if t.ni != nil {
		_ = t.ni.SetVisible(false)
		_ = t.ni.Dispose()
	}
	ni, err := walk.NewNotifyIcon(t.mw)
	if err != nil {
		t.cfg.Logger.Error("重建托盘图标失败", "err", err)
		return
	}
	t.ni = ni
	t.buildMenu()
	if err := t.applyIcon(); err != nil {
		t.cfg.Logger.Error("重建后设置图标失败", "err", err)
	}
	_ = t.ni.SetToolTip(t.StatusText())
	_ = t.ni.SetVisible(true)
	t.attachTrayClick()
	t.SetPaused(t.paused) // 重建菜单后恢复"暂停注入/恢复注入"文案
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

	autoEnter := walk.NewAction()
	autoEnter.SetText("自动回车")
	autoEnter.SetCheckable(true)
	if t.cfg.AutoEnterEnabled != nil {
		autoEnter.SetChecked(t.cfg.AutoEnterEnabled())
	}
	autoEnter.Triggered().Attach(t.toggleAutoEnterMenu)
	menu.Actions().Add(autoEnter)
	t.autoEnter = autoEnter

	logout := walk.NewAction()
	logout.SetText("退出登录")
	logout.Triggered().Attach(func() {
		if t.cfg.Logout != nil {
			t.cfg.Logout()
		}
	})
	menu.Actions().Add(logout)
	t.logoutAction = logout
	t.applyChannelMode() // 初始 ChannelNone

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

// SetChannelMode 切换通道模式（线程安全）。菜单不含任何绑定项：
// 未绑定时通过左键托盘或退出登录回到通道选择窗口。
func (t *Tray) SetChannelMode(m ChannelMode) {
	t.mw.Synchronize(func() {
		t.mode = m
		t.applyChannelMode()
	})
}

// applyChannelMode 按当前模式刷新菜单项可见性（须在 UI 线程调用）。
// 未绑定态隐藏"退出登录"（无会话可退），其余模式均可见。
func (t *Tray) applyChannelMode() {
	if t.logoutAction == nil {
		return
	}
	_ = t.logoutAction.SetVisible(t.mode != ChannelNone)
}

// toggleAutoEnterMenu 处理"自动回车"菜单点击：回调业务层切换，
// 再以返回值刷新勾选态（菜单项本身不自持状态，避免与持久化设置漂移）。
func (t *Tray) toggleAutoEnterMenu() {
	if t.cfg.ToggleAutoEnter == nil {
		return
	}
	_ = t.autoEnter.SetChecked(t.cfg.ToggleAutoEnter())
}

// Run 进入消息循环，阻塞至托盘退出。
func (t *Tray) Run() {
	t.mw.Run()
}

// CurrentChannelMode 返回当前通道模式（线程安全，经 UI 线程读取）。
func (t *Tray) CurrentChannelMode() ChannelMode {
	done := make(chan ChannelMode, 1)
	t.mw.Synchronize(func() { done <- t.mode })
	select {
	case v := <-done:
		return v
	case <-time.After(2 * time.Second):
		return ChannelNone
	}
}

// State 返回当前托盘状态（线程安全，经 UI 线程读取）。
func (t *Tray) State() TrayState {
	done := make(chan TrayState, 1)
	t.mw.Synchronize(func() { done <- t.state })
	select {
	case v := <-done:
		return v
	case <-time.After(2 * time.Second):
		return StateDisconnected
	}
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

// AutoEnterCheckedForTest 返回"自动回车"菜单项勾选态（供自动化测试）。线程安全。
func (t *Tray) AutoEnterCheckedForTest() bool {
	done := make(chan bool, 1)
	t.mw.Synchronize(func() {
		done <- t.autoEnter != nil && t.autoEnter.Checked()
	})
	select {
	case v := <-done:
		return v
	case <-time.After(2 * time.Second):
		return false
	}
}

// TriggerAutoEnterForTest 模拟点击"自动回车"菜单项（走完整切换回调链）。线程安全。
func (t *Tray) TriggerAutoEnterForTest() {
	t.mw.Synchronize(t.toggleAutoEnterMenu)
}

// TrayIconVisibleForTest 返回托盘图标当前可见态（供守护链路测试）。线程安全。
func (t *Tray) TrayIconVisibleForTest() bool {
	done := make(chan bool, 1)
	t.mw.Synchronize(func() {
		done <- t.ni != nil && t.ni.Visible()
	})
	select {
	case v := <-done:
		return v
	case <-time.After(2 * time.Second):
		return false
	}
}

// SimulateTaskbarRestartForTest 向托盘主窗口投递 TaskbarCreated，
// 模拟 explorer/任务栏重建（走真实子类化链路）。线程安全。
func (t *Tray) SimulateTaskbarRestartForTest() {
	t.mw.Synchronize(func() {
		win.PostTaskbarCreatedForTest(uintptr(t.mw.Handle()))
	})
}

// ShowChannelChooser 弹出通道选择窗口（未绑定态，微信/QQ 对等）。
// 线程安全。
func (t *Tray) ShowChannelChooser() {
	t.mw.Synchronize(func() {
		t.ensureChooserWindow()
		if t.chooserWin != nil {
			t.chooserWin.Show()
			_ = win.Activate(win.Hwnd(t.chooserWin.Handle()), 2*time.Second)
		}
	})
}

// ensureChooserWindow 惰性创建通道选择窗口（须在 UI 线程调用）。
func (t *Tray) ensureChooserWindow() {
	if t.chooserWin != nil {
		return
	}
	var w *walk.MainWindow
	err := MainWindow{
		AssignTo:   &w,
		Title:      "AirType · 选择绑定通道",
		Size:       Size{Width: 400, Height: 300},
		Background: SolidColorBrush{Color: walk.RGB(255, 255, 255)},
		Layout:     VBox{Margins: Margins{Left: 28, Top: 28, Right: 28, Bottom: 24}, Spacing: 14},
		Children: []Widget{
			Label{
				Text: "选择消息通道",
				Font: Font{Family: "Segoe UI", PointSize: 16, Bold: true},
			},
			Label{
				Text:      "扫码绑定后，手机上发消息即可在电脑隔空打字；换绑需先在托盘菜单退出登录",
				TextColor: walk.RGB(138, 143, 150),
			},
			Composite{
				Layout: HBox{Margins: Margins{Left: 0, Top: 8, Right: 0, Bottom: 0}, Spacing: 16},
				Children: []Widget{
					PushButton{
						AssignTo: nil,
						Text:     "💬 微信",
						MinSize:  Size{Width: 150, Height: 52},
						Font:     Font{Family: "Segoe UI", PointSize: 12},
						OnClicked: func() {
							w.Hide()
							if t.cfg.ChooseChannel != nil {
								t.cfg.ChooseChannel(ChoiceWeChat)
							}
						},
					},
					PushButton{
						Text:    "🐧 QQ 机器人",
						MinSize: Size{Width: 150, Height: 52},
						Font:    Font{Family: "Segoe UI", PointSize: 12},
						OnClicked: func() {
							w.Hide()
							if t.cfg.ChooseChannel != nil {
								t.cfg.ChooseChannel(ChoiceQQ)
							}
						},
					},
					HSpacer{},
				},
			},
			VSpacer{},
		},
	}.Create()
	if err != nil {
		t.cfg.Logger.Error("创建通道选择窗口失败", "err", err)
		return
	}
	t.chooserWin = w
	// 点 ✕ 只隐藏不销毁：walk 默认 WM_CLOSE 会 Dispose 窗口，
	// 而惰性创建指针不清空，之后 Show 静默失效（左键从此无响应）。
	w.Closing().Attach(func(canceled *bool, reason walk.CloseReason) {
		*canceled = true
		w.Hide()
	})
	win.RoundCorners(uintptr(w.Handle()))
}

// ShowQR 显示微信扫码二维码窗口（线程安全）。imageURL 为二维码内容链接。
func (t *Tray) ShowQR(imageURL string) {
	t.ShowQRWindow("扫码绑定微信", "用手机微信扫描下方二维码，授权后电脑端自动连接", imageURL)
}

// ShowQRWindow 显示可自定义标题/提示的扫码窗口（QQ 机器人绑定等场景）。
// 线程安全。
func (t *Tray) ShowQRWindow(title, hint, imageURL string) {
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
		_ = t.qrWin.SetTitle("AirType · " + title)
		_ = t.qrTitleLbl.SetText(title)
		_ = t.qrHintLbl.SetText(hint)
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
// 标题与提示文案由 ShowQRWindow 每次刷新（微信/QQ 共用此窗口）。
// 注意：walk 窗口带子控件必须设 Layout，否则 WM_SIZE 时 startLayout 崩溃。
func (t *Tray) ensureQRWindow() {
	if t.qrWin != nil {
		return
	}
	var w *walk.MainWindow
	var iv *walk.ImageView
	var ttl, hnt *walk.Label
	err := MainWindow{
		AssignTo:   &w,
		Title:      "AirType · 扫码绑定",
		Size:       Size{Width: 480, Height: 580},
		Background: SolidColorBrush{Color: walk.RGB(255, 255, 255)},
		Layout:     VBox{Margins: Margins{Left: 20, Top: 24, Right: 20, Bottom: 20}, Spacing: 14},
		Children: []Widget{
			Label{
				AssignTo: &ttl,
				Text:     "",
				Font:     Font{Family: "Segoe UI", PointSize: 15, Bold: true},
			},
			Label{
				AssignTo:  &hnt,
				Text:      "",
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
	t.qrTitleLbl = ttl
	t.qrHintLbl = hnt
	// 同通道选择窗口：点 ✕ 只隐藏不销毁；QQ 绑定流程据此取消
	w.Closing().Attach(func(canceled *bool, reason walk.CloseReason) {
		*canceled = true
		w.Hide()
		if t.cfg.QRDismissed != nil {
			go t.cfg.QRDismissed() // 回调里做通道切换（可能阻塞），不卡 UI 线程
		}
	})
	win.RoundCorners(uintptr(w.Handle()))
}

// applyIcon 按状态换图标（须在 UI 线程调用）。
// 语义：绿=一切正常；黄=连接异常/长时间未收到消息；红=未登录或需重新扫码。
// 暂停注入不单独换色，tooltip 会说明。
func (t *Tray) applyIcon() error {
	name := "red"
	switch t.state {
	case StateConnected:
		name = "green"
	case StateWarning, StateDisconnected:
		name = "yellow"
	}
	icon, err := stateIcon(name)
	if err != nil {
		return fmt.Errorf("ui: 加载图标 %s 失败: %w", name, err)
	}
	defer icon.Dispose()
	return t.ni.SetIcon(icon)
}
