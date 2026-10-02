//go:build windows

// traytest 在不接入微信、不需要管理员权限的情况下验证托盘与历史弹窗 UI：
// 循环切换状态；并演练"焦点记忆→弹窗→重注入"链路（配合 typetest-target 靶子窗口）。
//
// 时间线：
//   t=1s   NeedQR + 二维码窗口
//   t=6s   Connected；写入 3 条假历史
//   t=8s   激活 typetest-target（若在运行）
//   t=9s   打开历史弹窗（此刻捕获靶子窗口焦点快照）
//   t=12s  重注入第 0 条 → 弹窗收起 → 焦点还原到靶子 → 文字注入靶子
package main

import (
	"log/slog"
	"os"
	"time"

	"github.com/lxn/walk"
	"github.com/TeemoSun/AirType/internal/history"
	"github.com/TeemoSun/AirType/internal/typer"
	"github.com/TeemoSun/AirType/internal/ui"
	"github.com/TeemoSun/AirType/internal/win"
)

const targetTitle = "AirType Typetest Target"

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	hist, err := history.Open("traytest-history.json", 500)
	if err != nil {
		logger.Error("打开历史失败", "err", err)
		os.Exit(1)
	}

	tray, err := ui.NewTray(ui.Config{
		Logger:     logger,
		History:    hist,
		InjectText: typer.Type,
		TogglePause: func() bool {
			logger.Info("TogglePause 被调用")
			return time.Now().Second()%2 == 0
		},
		Rescan:  func() { logger.Info("Rescan 被调用") },
		Logout:  func() { logger.Info("Logout 被调用") },
		OpenLog: func() { logger.Info("OpenLog 被调用") },
		AutostartEnabled: func() bool { return false },
		AutostartSet:     func(bool) error { return nil },
	})
	if err != nil {
		logger.Error("创建托盘失败", "err", err)
		os.Exit(1)
	}
	hist.OnChange(tray.HistoryChanged)

	go scenario(logger, tray, hist)

	logger.Info("traytest 启动")
	tray.Run()
}

func scenario(logger *slog.Logger, tray *ui.Tray, hist *history.Store) {
	// 阶段 1：待扫码 + 二维码窗口
	tray.SetState(ui.StateNeedQR)
	tray.ShowQR("https://github.com/TeemoSun/AirType")
	time.Sleep(6 * time.Second)

	// 阶段 2：已连接，写入假历史
	tray.SetState(ui.StateConnected)
	tray.HideQR()
	hist.Add("帮我订明天下午三点的会议室，要带投屏的那种大房间")
	hist.Add("这个 bug 的根因是配置文件里少了一个逗号，已经修了")
	hist.Add("收到，我马上过去 😀")

	// 阶段 3：激活靶子窗口（若在运行）并打开历史弹窗
	time.Sleep(2 * time.Second)
	if hwnd := win.ByTitle(targetTitle); hwnd != 0 {
		if err := win.Activate(hwnd, 3*time.Second); err != nil {
			logger.Error("激活靶子窗口失败", "err", err)
		} else {
			logger.Info("靶子窗口已激活")
		}
	} else {
		logger.Warn("未找到靶子窗口，弹窗仍会正常打开")
	}
	time.Sleep(1 * time.Second)
	tray.ShowHistory()
	logger.Info("历史弹窗已打开（焦点快照已捕获）")

	// 阶段 4：复现用户报告 —— 打开 → 关闭(✕) → 再打开 → 检查是否闪关/标志卡死
	time.Sleep(2 * time.Second)
	callWithWatchdog(logger, tray.HidePopupForTest, "关闭弹窗(✕)")
	logger.Info("关闭后弹窗可见", "visible", tray.PopupIsShownForTest(), "flag", tray.PopupVisible())
	time.Sleep(1 * time.Second)
	callWithWatchdog(logger, tray.ShowHistory, "第二次打开弹窗")
	time.Sleep(3 * time.Second)
	logger.Info("二次打开3秒后", "visible", tray.PopupIsShownForTest(), "flag", tray.PopupVisible())
	callWithWatchdog(logger, func() { tray.SetLastReceived(time.Now()) }, "模拟收信(SetLastReceived)")

	// 阶段 5：剪贴板复制回读验证（模拟单击第 0 条）
	time.Sleep(500 * time.Millisecond)
	tray.PopupCopyForTest(0)
	time.Sleep(500 * time.Millisecond)
	got, err := walk.Clipboard().Text()
	if err != nil {
		logger.Error("剪贴板回读失败", "err", err)
	} else if got != "收到，我马上过去 😀" {
		logger.Error("剪贴板内容不符", "got", got)
	} else {
		logger.Info("剪贴板回读一致 ✓")
	}
	logger.Info("全部测试序列执行完毕")
}

// callWithWatchdog 执行 fn，若 3 秒未返回则记录死锁嫌疑并放弃等待。
func callWithWatchdog(logger *slog.Logger, fn func(), name string) {
	done := make(chan struct{})
	go func() {
		start := time.Now()
		fn()
		logger.Info("步骤完成", "name", name, "cost", time.Since(start).Round(time.Millisecond))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		logger.Error("步骤超时 —— UI 线程疑似死锁", "name", name)
	}
}
