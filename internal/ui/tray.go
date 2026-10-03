//go:build windows

// Package ui 实现托盘常驻界面：状态图标、右键菜单、扫码二维码窗口、
// 通道选择窗口、历史弹窗、气泡通知。与业务层通过回调解耦（见方案 §4.3）。
package ui

import (
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/lxn/walk"

	"github.com/TeemoSun/AirType/internal/history"
	"github.com/TeemoSun/AirType/internal/win"
)

// TrayState 是托盘显示的连接状态（与 bot.State 解耦）。
// 颜色语义按严重度单调递增：绿=正常 → 灰=未绑定/暂停 → 黄=需要注意
// （待扫码/连接异常/断开）→ 红=出错（凭据失效/启动失败）。
type TrayState int

const (
	StateIdle         TrayState = iota // 未绑定（弹通道选择窗口）
	StateConnected                     // 一切正常（绿）
	StateNeedQR                        // 待扫码/绑定中（黄）
	StateWarning                       // 连接异常/长时间未收到消息（黄）
	StateDisconnected                  // 连接断开，重连中（黄）
	StateError                         // 凭据失效/通道错误（红）
)

func (s TrayState) String() string {
	switch s {
	case StateConnected:
		return "已连接"
	case StateNeedQR:
		return "待扫码"
	case StateWarning:
		return "连接异常"
	case StateDisconnected:
		return "已断开"
	case StateError:
		return "登录已失效"
	default:
		return "未绑定"
	}
}

// ChannelMode 是当前绑定的消息通道（互斥：同时只能有一个）。
type ChannelMode int

const (
	ChannelNone   ChannelMode = iota // 未绑定（可任选微信或 QQ）
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
	// InjectText 把文本注入当前前台窗口（历史弹窗"重新打字"用）。
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
	// Logout 退出登录＝切换通道：清理当前通道凭据，回到通道选择窗口。
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

	chooserWin *walk.MainWindow
	qrWin      *walk.MainWindow
	qrCard     *qrCard
	qrTitleLbl *walk.Label
	qrHintLbl  *walk.Label
	qrShown    bool

	// uiThreadID 是创建主窗口（消息循环）的线程 ID，看门狗用它投递 WM_QUIT。
	uiThreadID uint32

	// 各惰性窗口创建时的深浅色；不一致时销毁重建，跟随系统主题切换。
	chooserDark, qrDark, popupDark bool
}

// NewTray 创建隐藏主窗口、托盘图标与右键菜单（不进入消息循环）。
func NewTray(cfg Config) (*Tray, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	t := &Tray{cfg: cfg, state: StateIdle}

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

	// 原生菜单深浅色跟随系统（托盘菜单/弹窗右键菜单）；uxtheme 桩指纹
	// 不符时内部静默放弃，仅影响观感
	win.EnableDarkMenus()
	t.buildMenu()
	t.attachTrayClick()

	// 托盘守护：walk 在 TaskbarCreated 后只以隐藏态重挂图标（实际仍不可见），
	// 且没有 NIM_ADD 级的公开恢复接口，图标一旦从托盘丢失便找不回来。
	// 这里子类化主窗口：任务栏重建或二次实例唤醒广播时，销毁重建图标。
	// onTrace 记录销毁类消息的到达：窗口若被外部 SendMessage 销毁（销毁发生
	// 在 GetMessage 内部派发的发送型消息里，事后无法取证），至少留下时间线。
	if err := win.SubclassTrayGuard(uintptr(t.mw.Handle()), t.onTaskbarRecreated, t.onWakeFromSecondInstance, t.traceDestructiveMsg); err != nil {
		t.cfg.Logger.Warn("安装托盘守护失败（不影响其余功能）", "err", err)
	}
	t.uiThreadID = win.CurrentThreadID()

	// 唤醒事件：命名内核对象，不受 UIPI 完整性级别限制（提权实例也能被
	// 普通权限的二次实例唤醒），也不依赖窗口存在（广播方案的盲区）。
	if wakeCh, err := win.ListenWake(); err == nil {
		go func() {
			for range wakeCh {
				t.mw.Synchronize(t.onWakeFromSecondInstance)
			}
		}()
	} else {
		t.cfg.Logger.Warn("创建唤醒事件失败（二次实例唤醒退回窗口广播）", "err", err)
	}

	return t, nil
}

// traceDestructiveMsg 记录可能销毁主窗口的消息（须轻量：运行在 UI 线程的
// 窗口过程里）。taskbarCreated/wake 两类由各自回调记录，这里不重复。
func (t *Tray) traceDestructiveMsg(msg uint32) {
	switch msg {
	case 0x0002 /*WM_DESTROY*/, 0x0010 /*WM_CLOSE*/, 0x0011, /*WM_QUERYENDSESSION*/
		0x0016 /*WM_ENDSESSION*/, 0x0082, /*WM_NCDESTROY*/
		0x001A /*WM_SETTINGCHANGE*/, 0x031E /*WM_THEMECHANGED*/ :
		t.cfg.Logger.Info("托盘主窗口收到窗口消息", "msg", fmt.Sprintf("0x%04X", msg))
	}
}

// StartWatchdog 窗口看门狗：每 2 秒向主窗口投递一条 WM_NULL，强制 walk 的
// 消息循环醒来复查 `fb.hWnd != 0` 退出条件——该条件只在取到投递型消息后
// 复查，若窗口被销毁于 GetMessage 内部派发的发送型消息里（外部 SendMessage
// 触发 WM_CLOSE 等），循环会永久阻塞成"无窗口僵尸"：进程活着、托盘没了、
// 单实例互斥量释放不掉，后续双击被拦且唤醒广播无窗口可收。
// Ping 失败（窗口已销毁）时向 UI 线程投递 WM_QUIT 走干净退出；300ms 宽限
// 是为了让正常退出路径（Run 返回后 close(done)）不产生误报。
// 必须在 NewTray 之后、Run 之前调用一次。
func (t *Tray) StartWatchdog(done <-chan struct{}) {
	hwnd := uintptr(t.mw.Handle())
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if win.PingWindow(hwnd) && win.IsWindow(hwnd) {
					continue
				}
				time.Sleep(300 * time.Millisecond) // 与正常退出路径的收尾竞态让路
				select {
				case <-done:
					return
				default:
				}
				if win.IsWindow(hwnd) {
					continue // 窗口重建等罕见竞态，继续观察
				}
				t.cfg.Logger.Error("主窗口已销毁但消息循环未退出（僵尸态），请求干净退出并释放单实例锁")
				win.PostQuitToThread(t.uiThreadID)
				return
			}
		}
	}()
}

// attachTrayClick 挂接托盘左键单击行为（图标重建后需对新图标重挂）。
// 左键单击：未绑定时弹通道选择窗口；待扫码时弹二维码窗口（没出来就
// 提示稍候）；有历史时弹窗做开关切换。
func (t *Tray) attachTrayClick() {
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
				return
			}
			_ = t.ni.ShowInfo("AirType", "正在获取二维码，请稍候再点…")
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
	t.SetPaused(t.paused) // 重建菜单后恢复"暂停打字/恢复打字"文案
}

// SetChannelMode 切换通道模式（线程安全）。菜单不含任何绑定项：
// 未绑定时通过左键托盘或"切换通道"回到通道选择窗口。
func (t *Tray) SetChannelMode(m ChannelMode) {
	t.mw.Synchronize(func() {
		t.mode = m
		t.applyChannelMode()
	})
}

// applyChannelMode 按当前模式刷新菜单项可见性（须在 UI 线程调用）。
// 未绑定态隐藏"切换通道"（无会话可退），其余模式均可见。
func (t *Tray) applyChannelMode() {
	if t.logoutAction == nil {
		return
	}
	_ = t.logoutAction.SetVisible(t.mode != ChannelNone)
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

// SetState 更新连接状态（线程安全）。
func (t *Tray) SetState(s TrayState) {
	t.mw.Synchronize(func() {
		t.state = s
		_ = t.applyIcon()
		_ = t.ni.SetToolTip(t.StatusText())
		if t.popup != nil {
			t.popup.updateHeader()
		}
	})
}

// SetPaused 更新暂停状态（线程安全）。
func (t *Tray) SetPaused(paused bool) {
	t.mw.Synchronize(func() {
		t.paused = paused
		if t.pauseAction != nil {
			if paused {
				t.pauseAction.SetText("恢复打字")
			} else {
				t.pauseAction.SetText("暂停打字")
			}
		}
		_ = t.applyIcon()
		_ = t.ni.SetToolTip(t.StatusText())
		if t.popup != nil {
			t.popup.updateHeader()
		}
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

// PopupFlashNew 弹窗打开期间收到新消息：头部短暂提示"新消息已入历史"，
// 免得用户以为消息丢了（静默跳过最伤信任）。线程安全。
func (t *Tray) PopupFlashNew() {
	t.mw.Synchronize(func() {
		if t.popup != nil && t.popup.win.Visible() {
			t.popup.flash("新消息已入历史")
		}
	})
}

// applyIcon 按状态换图标（须在 UI 线程调用）。
// 绿=正常；灰=未绑定；黄=待扫码/连接异常/断开/已暂停；红=凭据失效。
func (t *Tray) applyIcon() error {
	name := "gray"
	switch {
	case t.paused:
		name = "yellow" // 暂停需要被看见：黄=需要注意（tooltip 说明已暂停）
	case t.state == StateConnected:
		name = "green"
	case t.state == StateError:
		name = "red"
	case t.state == StateNeedQR, t.state == StateWarning, t.state == StateDisconnected:
		name = "yellow"
	}
	icon, err := stateIcon(name, t.ni.DPI())
	if err != nil {
		return fmt.Errorf("ui: 加载图标 %s 失败: %w", name, err)
	}
	defer icon.Dispose()
	return t.ni.SetIcon(icon)
}
