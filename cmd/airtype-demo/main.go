//go:build windows

// airtype-demo 是 M1 核心链路演示：终端二维码扫码登录，
// 收到微信文本消息后立即注入当前前台窗口。
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	ilinksdk "github.com/the-yex/wechat-ilink-sdk"
	"github.com/the-yex/wechat-ilink-sdk/ilink"
	"github.com/the-yex/wechat-ilink-sdk/login"

	"github.com/TeemoSun/AirType/internal/typer"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	dir := filepath.Join(os.Getenv("LOCALAPPDATA"), "AirType")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Error("创建数据目录失败", "dir", dir, "err", err)
		os.Exit(1)
	}
	store, err := login.NewFileTokenStore(dir)
	if err != nil {
		log.Error("创建 token 存储失败", "err", err)
		os.Exit(1)
	}
	log.Info("token 存储就绪", "dir", dir)

	c, err := ilinksdk.NewClient(
		ilinksdk.WithTokenStore(store),
		ilinksdk.WithLogger(log),
		ilinksdk.WithOnLogin(func(ctx context.Context, qr *login.QRCode) error {
			login.PrintQRCodeWithTerm(qr)
			return nil
		}),
	)
	if err != nil {
		log.Error("创建客户端失败", "err", err)
		os.Exit(1)
	}

	c.OnText(func(ctx context.Context, msg *ilink.Message, text string) error {
		received := time.Now()
		if err := typer.Type(text); err != nil {
			log.Error("注入失败", "msgID", msg.MessageID, "err", err)
			return err
		}
		log.Info("已注入", "msgID", msg.MessageID, "chars", len([]rune(text)),
			"createTimeDelta", received.Sub(time.UnixMilli(msg.CreateTimeMs)).Round(time.Millisecond))
		return nil
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	log.Info("启动，等待扫码或收消息…（Ctrl+C 退出）")
	if err := c.Run(ctx, nil); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("运行结束", "err", err)
		os.Exit(1)
	}
}
