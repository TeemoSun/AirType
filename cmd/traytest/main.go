//go:build windows

// traytest 在不接入微信、不需要管理员权限的情况下验证托盘 UI：
// 循环切换 状态/暂停/二维码窗口，观察图标颜色与窗口表现。
package main

import (
	"log/slog"
	"os"
	"time"

	"github.com/TeemoSun/AirType/internal/ui"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	tray, err := ui.NewTray(ui.Config{
		Logger: logger,
		TogglePause: func() bool {
			logger.Info("TogglePause 被调用")
			return time.Now().Second()%2 == 0
		},
		Rescan:  func() { logger.Info("Rescan 被调用") },
		OpenLog: func() { logger.Info("OpenLog 被调用") },
		AutostartEnabled: func() bool { return false },
		AutostartSet:     func(bool) error { return nil },
	})
	if err != nil {
		logger.Error("创建托盘失败", "err", err)
		os.Exit(1)
	}

	go func() {
		// 阶段 1：待扫码 + 二维码窗口
		tray.SetState(ui.StateNeedQR)
		tray.ShowQR("https://github.com/TeemoSun/AirType")
		time.Sleep(6 * time.Second)

		// 阶段 2：已连接，收信
		tray.SetState(ui.StateConnected)
		tray.HideQR()
		for i := 0; i < 3; i++ {
			time.Sleep(2 * time.Second)
			tray.SetLastReceived(time.Now())
		}

		// 阶段 3：暂停
		tray.SetPaused(true)
		time.Sleep(4 * time.Second)

		// 阶段 4：断开
		tray.SetPaused(false)
		tray.SetState(ui.StateDisconnected)
		time.Sleep(4 * time.Second)

		// 回到已连接，循环
		for {
			tray.SetState(ui.StateConnected)
			tray.SetLastReceived(time.Now())
			time.Sleep(10 * time.Second)
		}
	}()

	logger.Info("traytest 启动：状态循环 NeedQR→Connected→Paused→Disconnected")
	tray.Run()
}
