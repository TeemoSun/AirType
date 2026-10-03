//go:build windows

// qqtest 用给定的 AppID/AppSecret 直连 QQ 网关，验证 token/WS/事件链路：
//
//	qqtest -appid 123 -secret xxx [-seconds 30]
//
// 联调工具，不入产品流程；READY/C2C 事件均打印到控制台。
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/TeemoSun/AirType/internal/qqbot"
)

func main() {
	appID := flag.String("appid", "", "QQ 机器人 AppID")
	secret := flag.String("secret", "", "QQ 机器人 AppSecret")
	seconds := flag.Int("seconds", 30, "驻留秒数")
	flag.Parse()
	if *appID == "" || *secret == "" {
		fmt.Fprintln(os.Stderr, "用法: qqtest -appid <id> -secret <s> [-seconds 30]")
		os.Exit(2)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	dir, err := os.MkdirTemp("", "qqtest-*")
	if err != nil {
		logger.Error("临时目录失败", "err", err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)
	if err := qqbot.SaveCreds(dir, qqbot.Creds{AppID: *appID, AppSecret: *secret}); err != nil {
		logger.Error("写入临时凭据失败", "err", err)
		os.Exit(1)
	}

	b, err := qqbot.New(qqbot.Options{
		DataDir: dir,
		Logger:  logger,
		OnText: func(text string) error {
			logger.Info(">>> 收到消息并注入回调", "text", text)
			return nil
		},
		OnState: func(s qqbot.State) {
			logger.Info("状态", "state", s.String())
		},
	})
	if err != nil {
		logger.Error("创建 bot 失败", "err", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*seconds)*time.Second)
	defer cancel()
	logger.Info("qqtest 启动，连接网关…")
	err = b.Run(ctx)
	logger.Info("qqtest 退出", "err", err)
}
