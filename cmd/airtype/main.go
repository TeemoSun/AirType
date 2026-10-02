//go:build windows

// airtype 是控制台版入口（v1.0 MVP）：管理员权限运行，
// 扫码绑定微信 Bot，收到文本消息立即注入当前前台窗口。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/TeemoSun/AirType/internal/applog"
	"github.com/TeemoSun/AirType/internal/bot"
	"github.com/TeemoSun/AirType/internal/paths"
	"github.com/TeemoSun/AirType/internal/typer"
)

var version = "dev"

func main() {
	dataDir := flag.String("datadir", "", "数据目录（默认 %LOCALAPPDATA%\\AirType）")
	showVersion := flag.Bool("v", false, "显示版本号")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "AirType 隔空打字 %s\n\n", version)
		fmt.Fprintf(os.Stderr, "用法: airtype [选项]\n\n选项:\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *showVersion {
		fmt.Println("airtype", version)
		return
	}

	dir := *dataDir
	if dir == "" {
		var err error
		dir, err = paths.DataDir()
		if err != nil {
			fmt.Fprintln(os.Stderr, "错误:", err)
			os.Exit(1)
		}
	}

	logger, closer, err := applog.New(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
	defer closer()

	logger.Info("AirType 启动", "version", version, "datadir", dir,
		"token", filepath.Join(dir, "default.json"), "log", filepath.Join(dir, "airtype.log"))

	b, err := bot.New(bot.Options{
		DataDir: dir,
		Logger:  logger,
		OnText: func(text string) error {
			start := time.Now()
			if err := typer.Type(text); err != nil {
				return err
			}
			logger.Info("注入完成", "chars", len([]rune(text)), "cost", time.Since(start).Round(time.Millisecond))
			return nil
		},
		OnState: func(s bot.State) {
			fmt.Println("[状态]", s.String())
		},
	})
	if err != nil {
		logger.Error("初始化失败", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	fmt.Println("AirType 已启动：等待扫码或接收消息（Ctrl+C 退出）")
	if err := b.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("运行结束", "err", err)
		os.Exit(1)
	}
	b.Close()
	received, dropped := b.Stats()
	logger.Info("AirType 退出", "received", received, "dropped", dropped)
}
