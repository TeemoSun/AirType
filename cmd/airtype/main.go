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
	"sync"
	"sync/atomic"
	"time"

	"github.com/TeemoSun/AirType/internal/applog"
	"github.com/TeemoSun/AirType/internal/autostart"
	"github.com/TeemoSun/AirType/internal/bot"
	"github.com/TeemoSun/AirType/internal/paths"
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
		return 0 // 已有实例在跑，安静退出
	}

	a := &app{
		dir:    dir,
		logger: logger,
	}
	a.appCtx, a.appCancel = context.WithCancel(context.Background())
	a.paused.Store(false)

	tray, err := ui.NewTray(ui.Config{
		Logger:       logger,
		TogglePause:  a.togglePause,
		Rescan:       a.rescan,
		OpenLog:      func() { openExplorerSelect(filepath.Join(dir, "airtype.log")) },
		AutostartSet: autostart.Set,
		AutostartEnabled: func() bool {
			return autostart.Enabled()
		},
	})
	if err != nil {
		logger.Error("初始化托盘失败", "err", err)
		return 1
	}
	a.tray = tray

	logger.Info("AirType 启动", "version", version, "datadir", dir)

	if err := a.startBot(); err != nil {
		logger.Error("启动 bot 失败", "err", err)
		tray.NotifyError("AirType", "启动失败："+err.Error())
	}

	// 阻塞至托盘退出
	tray.Run()

	a.appCancel()
	a.stopBot()
	received, dropped := a.stats()
	logger.Info("AirType 退出", "received", received, "dropped", dropped)
	return 0
}

// app 聚合应用级状态与 bot 生命周期。
type app struct {
	dir    string
	logger *slog.Logger
	tray   *ui.Tray
	paused atomic.Bool

	appCtx    context.Context
	appCancel context.CancelFunc

	mu        sync.Mutex
	b         *bot.Bot
	runCancel context.CancelFunc
	runDone   chan struct{}
}

func (a *app) togglePause() bool {
	a.paused.Store(!a.paused.Load())
	p := a.paused.Load()
	a.logger.Info("切换暂停", "paused", p)
	return p
}

func (a *app) startBot() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	b, err := bot.New(bot.Options{
		DataDir: a.dir,
		Logger:  a.logger,
		OnText:  a.onText,
		OnQRCode: func(imageURL string) {
			a.tray.ShowQR(imageURL)
		},
		OnState: func(s bot.State) {
			switch s {
			case bot.StateConnected:
				a.tray.SetState(ui.StateConnected)
				a.tray.HideQR()
			case bot.StateSessionExpired:
				a.tray.SetState(ui.StateNeedQR)
			case bot.StateDisconnected:
				a.tray.SetState(ui.StateDisconnected)
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
			a.tray.SetState(ui.StateDisconnected)
		}
	}()
	return nil
}

func (a *app) stopBot() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.runCancel != nil {
		a.runCancel()
		<-a.runDone
		a.runCancel = nil
		a.runDone = nil
	}
	if a.b != nil {
		a.b.Close()
		a.b = nil
	}
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

func (a *app) onText(text string) error {
	received := time.Now()
	a.tray.SetLastReceived(received)
	if a.paused.Load() {
		a.logger.Info("已暂停，跳过注入", "chars", len([]rune(text)))
		return nil
	}
	start := time.Now()
	if err := typer.Type(text); err != nil {
		a.logger.Error("注入失败", "err", err)
		a.tray.NotifyError("AirType", "注入失败："+err.Error())
		return err
	}
	a.logger.Info("注入完成", "chars", len([]rune(text)),
		"injectCost", time.Since(start).Round(time.Millisecond))
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
