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

	logger, closer := applog.New(dir)
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
		ChooseChannel:   a.chooseChannel,
		QRDismissed:     a.qrDismissed,
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

	// 通道互斥且对等：QQ 已绑定只启 QQ；微信 token 在只启微信；
	// 都未绑定 → 弹通道选择窗口，由用户决定绑哪个。
	// 凭据文件损坏按"未绑定"处理并告警，避免落入"无通道无入口"死分支。
	_, qqBound, qqErr := qqbot.LoadCreds(dir)
	if qqErr != nil {
		logger.Warn("读取 QQ 凭据失败，按未绑定处理（文件可能损坏）", "err", qqErr)
	}
	if qqBound {
		tray.SetChannelMode(ui.ChannelQQ)
		if err := a.startQQ(false); err != nil {
			logger.Error("启动 QQ 机器人失败", "err", err)
			tray.NotifyError("AirType", "QQ 通道启动失败："+err.Error())
		}
	} else if _, err := os.Stat(filepath.Join(dir, "default.json")); err == nil {
		tray.SetChannelMode(ui.ChannelWeChat)
		if err := a.startBot(); err != nil {
			logger.Error("启动 bot 失败", "err", err)
			tray.NotifyError("AirType", "微信通道启动失败："+err.Error())
		}
	} else {
		tray.SetChannelMode(ui.ChannelNone)
		tray.ShowChannelChooser()
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

	// switchMu 串行化通道切换（选择/退出登录）：菜单回调在 UI 线程触发，
	// 停止会话可能等 2 秒兜底，不能卡 UI 线程，故在后台 goroutine 执行。
	switchMu sync.Mutex
}

// stopSession 取消会话并等待退出，2 秒兜底（SDK 长轮询退出偶发迟滞）。
func (a *app) stopSession(cancel context.CancelFunc, done <-chan struct{}, name string) {
	if cancel == nil {
		return
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		a.logger.Warn(name + " 未在 2 秒内退出，放弃等待")
	}
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
				// 过期不自动重弹微信二维码：回到通道选择窗口重新选择
				a.setWXState(ui.StateNeedQR)
				a.tray.SetChannelMode(ui.ChannelNone)
				a.tray.ShowChannelChooser()
				a.tray.NotifyInfo("AirType", "微信登录已过期；请重新选择通道绑定")
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
		// 与 QQ 通道对齐：微信 Run 异常退出（如开机自启时网络未就绪）
		// 也按退避自动重试，而不是静默死亡；凭据过期则由 OnState 走重新绑定。
		backoff := time.Second
		const maxBackoff = 30 * time.Second
		for {
			err := b.Run(ctx)
			if errors.Is(err, context.Canceled) {
				return
			}
			if b.State() == bot.StateSessionExpired {
				a.logger.Info("微信登录已过期，停止重试（等待重新绑定）")
				return
			}
			a.logger.Error("微信通道异常退出，稍后自动重试", "err", err, "backoff", backoff)
			a.setWXState(ui.StateDisconnected)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}()
	return nil
}

func (a *app) stopBot() {
	a.mu.Lock()
	cancel, done := a.runCancel, a.runDone
	b := a.b
	a.runCancel, a.runDone, a.b = nil, nil, nil
	a.mu.Unlock()
	// 等待期间不持锁：healthWatchdog 等仍可访问；重复调用拿到 nil 即快速返回
	a.stopSession(cancel, done, "微信通道")
	if b != nil {
		b.Close()
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
				// 凭据失效（如平台侧删除机器人）：清理并回到通道选择窗口
				_ = qqbot.ClearCreds(a.dir)
				a.qqState.Store(int32(qqbot.StateInit))
				a.tray.SetState(ui.StateNeedQR)
				a.tray.SetChannelMode(ui.ChannelNone)
				a.tray.ShowChannelChooser()
				a.tray.NotifyInfo("AirType", "QQ 凭据已失效；请重新选择通道绑定")
			}
		}
	}()
	return nil
}

// stopQQ 停止 QQ 通道（绑定流程与会话一并取消），2 秒兜底超时。
func (a *app) stopQQ() {
	a.mu.Lock()
	cancel, done := a.qqCancel, a.qqDone
	a.qqCancel, a.qqDone, a.qq = nil, nil, nil
	a.mu.Unlock()
	a.stopSession(cancel, done, "QQ 通道")
}

// chooseChannel 通道选择窗口回调：微信/QQ 完全对等，选择后停掉
// 现有会话进入对应绑定流程（微信弹扫码，QQ 先申请绑定二维码）。
// 停止会话可能等 2 秒兜底，放后台执行避免卡 UI 线程。
func (a *app) chooseChannel(c ui.ChannelChoice) {
	go a.switchChannel(c)
}

func (a *app) switchChannel(c ui.ChannelChoice) {
	a.switchMu.Lock()
	defer a.switchMu.Unlock()
	a.logger.Info("选择绑定通道", "channel", c.String())
	a.stopBot()
	a.stopQQ()
	// 通道状态归零：避免残留的 Connected 在后续刷新里把托盘"假绿"
	a.wxState.Store(int32(ui.StateNeedQR))
	a.qqState.Store(int32(qqbot.StateInit))
	a.tray.SetState(ui.StateNeedQR)
	if c == ui.ChoiceQQ {
		if err := a.startQQ(true); err != nil {
			a.logger.Error("启动 QQ 绑定流程失败", "err", err)
			a.tray.NotifyError("AirType", "QQ 绑定启动失败："+err.Error())
		}
		return
	}
	// 微信：清掉可能残留的失效 token，重新走扫码
	if err := os.Remove(filepath.Join(a.dir, "default.json")); err != nil && !os.IsNotExist(err) {
		a.logger.Warn("清理微信 token 失败", "err", err)
	}
	if err := a.startBot(); err != nil {
		a.logger.Error("启动微信绑定失败", "err", err)
		a.tray.NotifyError("AirType", "微信通道启动失败："+err.Error())
	}
}

// qrDismissed 用户关掉扫码窗口：QQ 绑定流程取消并回到通道选择；
// 微信扫码只隐藏窗口（SDK 继续等待，左键托盘可重开二维码）。
func (a *app) qrDismissed() {
	a.switchMu.Lock()
	defer a.switchMu.Unlock()
	a.mu.Lock()
	qqActive := a.qq != nil
	a.mu.Unlock()
	if !qqActive {
		return
	}
	a.logger.Info("用户关闭扫码窗，取消 QQ 绑定流程")
	a.stopQQ()
	a.qqState.Store(int32(qqbot.StateInit))
	a.tray.SetState(ui.StateNeedQR)
	a.tray.ShowChannelChooser()
}

// logout 退出登录（按当前绑定的通道清理），回到未绑定状态并弹通道
// 选择窗口重新选择；换绑的唯一入口。后台执行避免卡 UI 线程。
// （平台侧的授权关系无公开 API 可删，如需彻底移除请在手机端操作。）
func (a *app) logout() {
	go func() {
		a.switchMu.Lock()
		defer a.switchMu.Unlock()
		mode := a.tray.CurrentChannelMode()
		if mode == ui.ChannelNone {
			// 防御：菜单项在未绑定态本就隐藏；文件判断兜底
			if _, qqBound, _ := qqbot.LoadCreds(a.dir); qqBound {
				mode = ui.ChannelQQ
			} else {
				mode = ui.ChannelWeChat
			}
		}
		if mode == ui.ChannelQQ {
			a.logger.Info("退出登录（QQ 通道）：停止会话并删除凭据")
			a.stopQQ()
			if err := qqbot.ClearCreds(a.dir); err != nil {
				a.logger.Error("删除 QQ 凭据失败", "err", err)
			}
			a.qqState.Store(int32(qqbot.StateInit))
		} else {
			a.logger.Info("退出登录（微信通道）：停止 bot 并删除本地 token")
			a.stopBot()
			if err := os.Remove(filepath.Join(a.dir, "default.json")); err != nil && !os.IsNotExist(err) {
				a.logger.Error("删除 token 失败", "err", err)
			}
			a.wxState.Store(int32(ui.StateNeedQR))
		}
		a.tray.HideQR()
		a.tray.SetState(ui.StateNeedQR)
		a.tray.SetChannelMode(ui.ChannelNone)
		a.tray.NotifyInfo("AirType", "已退出登录；可重新选择绑定通道")
		a.tray.ShowChannelChooser()
	}()
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
		a.tray.NotifyInfo("AirType", "当前前台是桌面，没有可输入的地方；消息已存入历史，可左键托盘打开历史复制")
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

// stats 汇总两个通道的收信/去重计数（任一在跑即计入）。
func (a *app) stats() (received, dropped int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.b != nil {
		r, d := a.b.Stats()
		received += r
		dropped += d
	}
	if a.qq != nil {
		r, d := a.qq.Stats()
		received += r
		dropped += d
	}
	return received, dropped
}

func openExplorerSelect(path string) {
	_ = exec.Command("explorer", "/select,"+path).Start()
}
