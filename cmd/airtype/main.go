//go:build windows

// airtype 是 v1.1 托盘常驻版入口：无控制台，常驻托盘，
// 扫码 GUI 化，收到文本消息立即注入当前前台窗口。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/TeemoSun/AirType/internal/applog"
	"github.com/TeemoSun/AirType/internal/autostart"
	"github.com/TeemoSun/AirType/internal/bot"
	"github.com/TeemoSun/AirType/internal/history"
	"github.com/TeemoSun/AirType/internal/paths"
	"github.com/TeemoSun/AirType/internal/qqbot"
	"github.com/TeemoSun/AirType/internal/settings"
	"github.com/TeemoSun/AirType/internal/typer"
	"github.com/TeemoSun/AirType/internal/ui"
	"github.com/TeemoSun/AirType/internal/win"
)

var version = "dev"

func main() {
	dataDir := flag.String("datadir", "", "数据目录（默认 %LOCALAPPDATA%\\AirType）")
	showVersion := flag.Bool("v", false, "显示版本号")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "AirType 隔空打字", version)
		fmt.Fprintln(os.Stderr, "\n用法: airtype [选项]\n\n选项:")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *showVersion {
		fmt.Println("airtype", version)
		return
	}

	os.Exit(run(*dataDir))
}

func run(dataDir string) int {
	dir := dataDir
	if dir == "" {
		var err error
		dir, err = paths.DataDir()
		if err != nil {
			fmt.Fprintln(os.Stderr, "错误:", err)
			return 1
		}
	}

	logger, closer, err := applog.New(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		return 1
	}
	defer closer()

	if err := win.AcquireSingleInstance("AirType"); err != nil {
		logger.Error("单实例检查失败", "err", err)
		// 唤醒已在运行的实例：重建托盘图标并弹提示。
		// 之前"安静退出"会让用户误以为没启动（尤其图标意外丢失时），双击反而成了找回图标的手势。
		win.WakeRunningInstance()
		return 0 // 已有实例在跑，本进程退出
	}

	hist, err := history.Open(filepath.Join(dir, "history.json"), 500)
	if err != nil {
		logger.Error("打开历史存储失败", "err", err)
		return 1
	}
	defer func() { _ = hist.Close() }()

	a := &app{
		dir:    dir,
		logger: logger,
		hist:   hist,
	}
	a.appCtx, a.appCancel = context.WithCancel(context.Background())
	a.paused.Store(false)

	// 用户偏好（settings.json）；加载失败不阻断启动，用默认值
	prefs, err := settings.Load(dir)
	if err != nil {
		logger.Warn("加载设置失败，使用默认值", "err", err)
	}
	a.autoEnter.Store(prefs.AutoEnter)

	tray, err := ui.NewTray(ui.Config{
		Logger:      logger,
		History:     hist,
		InjectText:  a.inject,
		TogglePause: a.togglePause,
		AutoEnterEnabled: func() bool {
			return a.autoEnter.Load()
		},
		ToggleAutoEnter: a.toggleAutoEnter,
		Rescan:          a.rescan,
		BindQQ:          a.bindQQ,
		Logout:          a.logout,
		OpenLog:         func() { openExplorerSelect(filepath.Join(dir, "airtype.log")) },
		AutostartSet:    autostart.Set,
		AutostartEnabled: func() bool {
			return autostart.Enabled()
		},
	})
	if err != nil {
		logger.Error("初始化托盘失败", "err", err)
		return 1
	}
	a.tray = tray
	hist.OnChange(tray.HistoryChanged)

	logger.Info("AirType 启动", "version", version, "datadir", dir)

	// 健康巡检：已连接但长时间收不到消息 → 黄色（网络/通道异常自检，方案 §7）
	go healthWatchdog(a.appCtx, a)

	// 通道互斥：QQ 已绑定 → 只启动 QQ；否则走微信（含首启扫码）。
	if _, qqBound, err := qqbot.LoadCreds(dir); err != nil {
		logger.Warn("读取 QQ 凭据失败", "err", err)
	} else if qqBound {
		tray.SetChannelMode(ui.ChannelQQ)
		if err := a.startQQ(false); err != nil {
			logger.Error("启动 QQ 机器人失败", "err", err)
			tray.NotifyError("AirType", "QQ 通道启动失败："+err.Error())
		}
	} else {
		// 有微信 token = 已绑定微信；没有 = 未绑定（可任选通道）
		if _, err := os.Stat(filepath.Join(dir, "default.json")); err == nil {
			tray.SetChannelMode(ui.ChannelWeChat)
		} else {
			tray.SetChannelMode(ui.ChannelNone)
		}
		if err := a.startBot(); err != nil {
			logger.Error("启动 bot 失败", "err", err)
			tray.NotifyError("AirType", "启动失败："+err.Error())
		}
	}

	// 阻塞至托盘退出
	tray.Run()

	a.appCancel()
	a.stopBot()
	a.stopQQ()
	received, dropped := a.stats()
	logger.Info("AirType 退出", "received", received, "dropped", dropped)
	return 0
}

// app 聚合应用级状态与 bot 生命周期。
type app struct {
	dir    string
	logger *slog.Logger
	tray   *ui.Tray
	hist   *history.Store
	paused atomic.Bool

	// autoEnter：消息注入完成后自动补一次回车（发送）。持久化到 settings.json。
	autoEnter atomic.Bool

	// 双通道状态（int32 存枚举），refreshTrayState 据此合并托盘颜色。
	wxState atomic.Int32 // ui.TrayState
	qqState atomic.Int32 // qqbot.State

	appCtx    context.Context
	appCancel context.CancelFunc

	mu        sync.Mutex
	b         *bot.Bot
	runCancel context.CancelFunc
	runDone   chan struct{}

	qq       *qqbot.Bot
	qqCancel context.CancelFunc
	qqDone   chan struct{}
}

// refreshTrayState 合并微信/QQ 两通道状态决定托盘颜色：
// 任一通道已连接即绿（至少一个通道可用）；否则任一通道异常/断连即黄；
// 都未登录则红。微信的看门狗黄/收信恢复绿也走这里。
func (a *app) refreshTrayState() {
	wx := ui.TrayState(a.wxState.Load())
	qq := qqbot.State(a.qqState.Load())
	switch {
	case wx == ui.StateConnected || qq == qqbot.StateConnected:
		a.tray.SetState(ui.StateConnected)
	case wx == ui.StateWarning || wx == ui.StateDisconnected,
		qq == qqbot.StateDisconnected || qq == qqbot.StateSessionExpired:
		a.tray.SetState(ui.StateWarning)
	default:
		a.tray.SetState(ui.StateNeedQR)
	}
}

func (a *app) setWXState(s ui.TrayState) {
	a.wxState.Store(int32(s))
	a.refreshTrayState()
}

// staleAfter 是"已连接但多久没收到消息算异常"的阈值。
// 长轮询没有心跳，只能靠收信时间推断（方案 §7 静默失败自检）。
const staleAfter = 10 * time.Minute

// healthWatchdog 周期检查：已连接却超过 staleAfter 没收到任何消息 → 转黄。
// 收到新消息或状态事件会转回绿色/对应颜色。
func healthWatchdog(ctx context.Context, a *app) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.mu.Lock()
			b := a.b
			a.mu.Unlock()
			if b == nil || b.State() != bot.StateConnected || a.paused.Load() {
				continue
			}
			if last := b.LastReceivedAt(); !last.IsZero() && time.Since(last) > staleAfter {
				a.setWXState(ui.StateWarning)
			}
		}
	}
}

func (a *app) togglePause() bool {
	a.paused.Store(!a.paused.Load())
	p := a.paused.Load()
	a.logger.Info("切换暂停", "paused", p)
	return p
}

// toggleAutoEnter 切换"自动回车"并立即持久化，返回切换后的状态。
func (a *app) toggleAutoEnter() bool {
	v := !a.autoEnter.Load()
	a.autoEnter.Store(v)
	if err := settings.Save(a.dir, settings.Settings{AutoEnter: v}); err != nil {
		// 持久化失败只记日志：本次会话开关仍生效，重启后回退旧值
		a.logger.Error("保存设置失败", "err", err)
	}
	a.logger.Info("切换自动回车", "enabled", v)
	return v
}

func (a *app) startBot() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	b, err := bot.New(bot.Options{
		DataDir: a.dir,
		Logger:  a.logger,
		OnText:  a.onWxText,
		OnQRCode: func(imageURL string) {
			a.tray.ShowQR(imageURL)
		},
		OnState: func(s bot.State) {
			switch s {
			case bot.StateConnected:
				a.setWXState(ui.StateConnected)
				a.tray.SetChannelMode(ui.ChannelWeChat) // 扫码成功即锁定微信通道
				a.tray.HideQR()
			case bot.StateSessionExpired:
				a.setWXState(ui.StateNeedQR) // 红：登录过期，需重新扫码
			case bot.StateDisconnected:
				a.setWXState(ui.StateWarning) // 黄：连不上服务器（token 仍有效）
			}
		},
	})
	if err != nil {
		return err
	}
	a.b = b

	ctx, cancel := context.WithCancel(a.appCtx)
	done := make(chan struct{})
	a.runCancel = cancel
	a.runDone = done
	go func() {
		defer close(done)
		if err := b.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			a.logger.Error("bot 运行结束", "err", err)
			a.setWXState(ui.StateDisconnected)
		}
	}()
	return nil
}

func (a *app) stopBot() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.runCancel != nil {
		a.runCancel()
		// 兜底：SDK 长轮询退出偶发迟滞，2 秒后放弃等待，避免退出挂死
		select {
		case <-a.runDone:
		case <-time.After(2 * time.Second):
			a.logger.Warn("bot 未在 2 秒内退出，放弃等待（进程仍将退出）")
		}
		a.runCancel = nil
		a.runDone = nil
	}
	if a.b != nil {
		a.b.Close()
		a.b = nil
	}
}

// startQQ 启动 QQ 通道。withBind=true 时先走扫码绑定再连网关；
// 否则要求已有凭据（未绑定时返回 ErrNotBound）。
func (a *app) startQQ(withBind bool) error {
	b, err := qqbot.New(qqbot.Options{
		DataDir: a.dir,
		Logger:  a.logger,
		OnText:  a.onText,
		OnState: func(s qqbot.State) {
			a.qqState.Store(int32(s))
			a.refreshTrayState()
		},
		OnQRCode: func(u string) {
			a.tray.ShowQRWindow("扫码绑定 QQ 机器人",
				"用手机 QQ 扫描下方二维码，确认后电脑端自动连接", u)
		},
		OnBound: func(appID string) {
			// 通道互斥：QQ 绑定成功即清理微信 token，锁定 QQ 通道
			if err := os.Remove(filepath.Join(a.dir, "default.json")); err != nil && !os.IsNotExist(err) {
				a.logger.Warn("清理微信 token 失败", "err", err)
			}
			a.wxState.Store(int32(ui.StateNeedQR))
			a.tray.SetChannelMode(ui.ChannelQQ)
			a.tray.HideQR()
			a.tray.NotifyInfo("AirType", "QQ 机器人绑定成功（AppID "+appID+"），正在连接…")
		},
	})
	if err != nil {
		return err
	}
	if !withBind && !b.Bound() {
		return qqbot.ErrNotBound
	}
	ctx, cancel := context.WithCancel(a.appCtx)
	done := make(chan struct{})
	a.mu.Lock()
	defer a.mu.Unlock()
	a.qq, a.qqCancel, a.qqDone = b, cancel, done
	go func() {
		defer close(done)
		if withBind {
			if _, err := b.Bind(ctx); err != nil {
				if !errors.Is(err, context.Canceled) {
					a.logger.Error("QQ 绑定失败", "err", err)
					a.tray.HideQR()
					a.tray.NotifyError("AirType", "QQ 绑定失败："+err.Error())
				}
				return
			}
		}
		if err := b.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			a.logger.Error("QQ 会话结束", "err", err)
			if b.State() == qqbot.StateSessionExpired {
				a.tray.NotifyInfo("AirType", "QQ 机器人凭据已失效；请通过\"绑定 QQ 机器人\"重新扫码")
			}
		}
	}()
	return nil
}

// stopQQ 停止 QQ 通道（绑定流程与会话一并取消），2 秒兜底超时。
func (a *app) stopQQ() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.qqCancel != nil {
		a.qqCancel()
		select {
		case <-a.qqDone:
		case <-time.After(2 * time.Second):
			a.logger.Warn("QQ 通道未在 2 秒内退出，放弃等待")
		}
		a.qqCancel = nil
		a.qqDone = nil
	}
	if a.qq != nil {
		a.qq = nil
	}
}

// bindQQ 托盘"绑定 QQ 机器人"入口。通道互斥：微信已登录时拒绝
// （换绑需先"退出登录"）；QQ 已绑定时为同通道重绑。绑定成功即
// 停用微信通道并清理其 token。
func (a *app) bindQQ() {
	if _, wxTokenErr := os.Stat(filepath.Join(a.dir, "default.json")); wxTokenErr == nil && a.wxLoggedIn() {
		a.tray.NotifyInfo("AirType", "当前已登录微信通道；换绑请先\"退出登录\"")
		return
	}
	a.logger.Info("开始绑定 QQ 机器人")
	a.stopBot() // 未登录微信时仅停轮询，避免二维码期间无谓请求
	a.stopQQ()
	if err := a.startQQ(true); err != nil {
		a.logger.Error("启动 QQ 绑定流程失败", "err", err)
		a.tray.NotifyError("AirType", "QQ 绑定启动失败："+err.Error())
	}
}

// wxLoggedIn 报告微信 bot 当前是否处于已登录（连接/断开）状态。
func (a *app) wxLoggedIn() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.b != nil && a.b.State() != bot.StateInit && a.b.State() != bot.StateSessionExpired
}

// rescan 停止 bot、删除 token、重启走扫码流程。
func (a *app) rescan() {
	a.logger.Info("重新扫码")
	a.stopBot()
	if err := os.Remove(filepath.Join(a.dir, "default.json")); err != nil && !os.IsNotExist(err) {
		a.logger.Error("删除 token 失败", "err", err)
	}
	a.tray.SetState(ui.StateNeedQR)
	if err := a.startBot(); err != nil {
		a.logger.Error("重启 bot 失败", "err", err)
		a.tray.NotifyError("AirType", "重新扫码失败："+err.Error())
	}
}

// logout 退出登录（按当前绑定的通道清理），回到未绑定状态，
// 之后可任选微信或 QQ 重新绑定。
// （平台侧的授权关系无公开 API 可删，如需彻底移除请在手机端操作。）
func (a *app) logout() {
	if _, qqBound, _ := qqbot.LoadCreds(a.dir); qqBound {
		a.logger.Info("退出登录（QQ 通道）：停止会话并删除凭据")
		a.stopQQ()
		if err := qqbot.ClearCreds(a.dir); err != nil {
			a.logger.Error("删除 QQ 凭据失败", "err", err)
		}
		a.tray.HideQR()
		a.wxState.Store(int32(ui.StateNeedQR))
		a.qqState.Store(int32(qqbot.StateInit))
		a.tray.SetState(ui.StateNeedQR)
		a.tray.SetChannelMode(ui.ChannelNone)
		a.tray.NotifyInfo("AirType", "QQ 通道已退出；可重新绑定 QQ 或微信")
		return
	}
	a.logger.Info("退出登录（微信通道）：停止 bot 并删除本地 token")
	a.stopBot()
	if err := os.Remove(filepath.Join(a.dir, "default.json")); err != nil && !os.IsNotExist(err) {
		a.logger.Error("删除 token 失败", "err", err)
	}
	a.tray.HideQR()
	a.tray.SetState(ui.StateNeedQR)
	a.tray.SetChannelMode(ui.ChannelNone)
	a.tray.NotifyInfo("AirType", "微信通道已退出；可扫码绑定微信或 QQ")
}

// onWxText 微信通道收信：先做通道健康恢复（黄色巡检态→绿），
// 再走共用处理。QQ 通道直接用 onText（不影响微信侧状态）。
func (a *app) onWxText(text string) error {
	a.setWXState(ui.StateConnected)
	return a.onText(text)
}

func (a *app) onText(text string) error {
	a.tray.SetLastReceived(time.Now())
	a.hist.Add(text) // 暂停时也记录：消息不丢，事后可从历史弹窗补发
	if a.paused.Load() {
		a.logger.Info("已暂停，只记录不注入", "chars", len([]rune(text)))
		return nil
	}
	// 弹窗打开期间注入会打进弹窗自身：只入历史（列表实时刷新），不注入
	if a.tray.PopupVisible() {
		a.logger.Info("弹窗打开，跳过注入（已入历史，可复制）", "chars", len([]rune(text)))
		return nil
	}
	return a.inject(text)
}

// inject 注入当前前台窗口，失败时气泡提示。注入前后都记录目标窗口信息，
// 便于定位"收到消息但没打字"类问题（按键到底去了哪里）。
func (a *app) inject(text string) error {
	fgTitle, fgExe := win.ForegroundInfo()
	// 前台是桌面：无处输入，明确提醒而不是无声丢失
	if fgTitle == "Program Manager" && strings.HasSuffix(strings.ToLower(fgExe), "explorer.exe") {
		a.logger.Warn("前台是桌面，无输入框，跳过注入", "text已入历史", true)
		a.tray.NotifyInfo("AirType", "当前前台是桌面，没有可输入的地方；请把焦点切到输入框后，从历史弹窗单击重发")
		return nil
	}
	start := time.Now()
	err := typer.Type(text)
	if err != nil {
		a.logger.Error("注入失败", "err", err, "fgWindow", fgTitle, "fgProc", fgExe)
		a.tray.NotifyError("AirType", "注入失败："+err.Error())
		return err
	}
	// 自动回车：文字已在输入框里，再补一次回车把消息发送出去。
	// 失败只记日志（文字本体已注入，用户手按一次回车即可补救）。
	if a.autoEnter.Load() {
		if err := typer.PressEnter(); err != nil {
			a.logger.Error("自动回车失败", "err", err, "fgWindow", fgTitle, "fgProc", fgExe)
			return err
		}
	}
	a.logger.Info("注入完成", "chars", len([]rune(text)),
		"autoEnter", a.autoEnter.Load(),
		"injectCost", time.Since(start).Round(time.Millisecond),
		"fgWindow", fgTitle, "fgProc", fgExe)
	return nil
}

func (a *app) stats() (received, dropped int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.b != nil {
		return a.b.Stats()
	}
	return 0, 0
}

func openExplorerSelect(path string) {
	_ = exec.Command("explorer", "/select,"+path).Start()
}
