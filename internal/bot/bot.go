// Package bot 封装 iLink 客户端：token 持久化、消息去重、
// 状态与最后收信时间跟踪，并把文本消息回调交由上层注入。
package bot

import (
	"context"
	"fmt"
	"hash/fnv"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	ilinksdk "github.com/the-yex/wechat-ilink-sdk"
	"github.com/the-yex/wechat-ilink-sdk/event"
	"github.com/the-yex/wechat-ilink-sdk/ilink"
	"github.com/the-yex/wechat-ilink-sdk/login"
)

// State 表示与微信服务端的连接状态。
type State int

const (
	StateInit State = iota
	StateConnected
	StateSessionExpired
	StateDisconnected
)

func (s State) String() string {
	switch s {
	case StateConnected:
		return "connected"
	case StateSessionExpired:
		return "session-expired"
	case StateDisconnected:
		return "disconnected"
	default:
		return "init"
	}
}

// Options 配置 Bot。DataDir 与 Logger 必填；OnText 为收到新消息时的回调。
type Options struct {
	DataDir string
	Logger  *slog.Logger

	// OnText 收到去重后的新文本消息时回调（通常为 typer.Type）。
	// 返回错误只记日志，不影响轮询循环。
	OnText func(text string) error

	// OnQRCode 需要扫码登录时回调（参数为二维码图片 URL）；
	// 为 nil 时在终端打印 ASCII 二维码。
	OnQRCode func(imageURL string)

	// OnState 状态变化回调（connected / session-expired / disconnected）。
	OnState func(state State)
}

// Bot 包装 iLink 客户端的运行时。
type Bot struct {
	opts   Options
	client *ilinksdk.Client
	dedup  *deduper

	mu            sync.Mutex
	lastReceived  time.Time
	state         State
	receivedCount int
	droppedCount  int
}

// New 创建 Bot 并完成 token 存储与回调装配。
func New(opts Options) (*Bot, error) {
	if opts.DataDir == "" {
		return nil, fmt.Errorf("bot: DataDir 不能为空")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	store, err := newDPAPITokenStore(opts.DataDir)
	if err != nil {
		return nil, fmt.Errorf("bot: 创建 token 存储失败: %w", err)
	}

	b := &Bot{
		opts:  opts,
		dedup: newDeduper(500),
	}

	c, err := ilinksdk.NewClient(
		ilinksdk.WithTokenStore(store),
		ilinksdk.WithLogger(opts.Logger),
		// 会话过期时不走 SDK 默认的自动重登（会直接弹微信二维码）：
		// 返回 nil 结果让轮询循环停止，由应用层回到通道选择窗口。
		ilinksdk.WithOnSessionExpired(func(ctx context.Context) (*ilink.LoginResult, error) {
			return nil, nil
		}),
		// 网络抖动下的取信延迟优化：
		// 1) 轮询失败退避封顶 30s→8s：坏网络时消息取回空窗显著收窄，
		//    单客户端对腾讯服务器的高频重试完全可接受
		ilinksdk.WithPollErrorBackoff(time.Second, 8*time.Second),
		// 2) 长轮询连接换用带 TCP keepalive 的客户端（SDK 默认 Transport
		//    未配 Dialer，keepalive 关闭）：5s 探测包既让 NAT/防火墙映射
		//    不被回收（家用 Wi-Fi 静默掐线的主因），也能尽早发现死连接
		ilinksdk.WithLongPollHTTPClient(longPollClient()),
		ilinksdk.WithOnLogin(func(ctx context.Context, qr *login.QRCode) error {
			if opts.OnQRCode != nil {
				opts.OnQRCode(qr.ImageURL)
				return nil
			}
			login.PrintQRCodeWithTerm(qr)
			return nil
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("bot: 创建客户端失败: %w", err)
	}
	b.client = c

	c.OnText(func(ctx context.Context, msg *ilink.Message, text string) error {
		key := msgKey(msg)
		if !b.dedup.check(key) {
			b.mu.Lock()
			b.droppedCount++
			b.mu.Unlock()
			opts.Logger.Warn("重复消息已忽略", "key", key, "msgID", msg.MessageID)
			return nil
		}
		b.mu.Lock()
		b.lastReceived = time.Now()
		b.receivedCount++
		b.mu.Unlock()

		if err := opts.OnText(text); err != nil {
			opts.Logger.Error("消息处理失败", "key", key, "err", err)
			return err
		}
		opts.Logger.Info("消息已处理", "key", key, "chars", len([]rune(text)),
			"age", time.Since(time.UnixMilli(msg.CreateTimeMs)).Round(time.Millisecond))
		return nil
	})

	c.Events().Subscribe(event.EventTypeConnected, b.onEvent)
	c.Events().Subscribe(event.EventTypeDisconnected, b.onEvent)
	c.Events().Subscribe(event.EventTypeSessionExpired, b.onEvent)
	// 登录成功也视为已连接（Connected 兜底）：确保二维码窗口被收起
	c.Events().Subscribe(event.EventTypeLogin, b.onEvent)

	return b, nil
}

// longPollClient 构造 getupdates 长轮询专用 HTTP 客户端。
// 40s 总超时略高于服务端 35s 挂起时长（longpolling_timeout_ms），避免
// 客户端抢先中止把"正常空返回"变成失败（openclaw-weixin#75 的同类坑）；
// 5s TCP keepalive 见 New 里的注释。不设 ResponseHeaderTimeout——
// 长轮询在挂起期间连响应头都不发，只能靠总超时兜底。
func longPollClient() *http.Client {
	d := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 5 * time.Second}
	return &http.Client{
		Timeout: 40 * time.Second,
		Transport: &http.Transport{
			DialContext:         d.DialContext,
			MaxIdleConns:        10,
			MaxIdleConnsPerHost: 4,
			IdleConnTimeout:     90 * time.Second,
			TLSHandshakeTimeout: 10 * time.Second,
		},
	}
}

func (b *Bot) onEvent(_ context.Context, ev *event.Event) error {
	switch ev.Type {
	case event.EventTypeConnected, event.EventTypeLogin:
		b.setState(StateConnected)
	case event.EventTypeDisconnected:
		b.setState(StateDisconnected)
	case event.EventTypeSessionExpired:
		b.setState(StateSessionExpired)
	}
	return nil
}

func (b *Bot) setState(s State) {
	b.mu.Lock()
	prev := b.state
	b.state = s
	b.mu.Unlock()
	if prev != s {
		b.opts.Logger.Info("状态变化", "from", prev.String(), "to", s.String())
		if b.opts.OnState != nil {
			b.opts.OnState(s)
		}
	}
}

// Run 阻塞运行（扫码登录 + 长轮询），ctx 取消或 Close 后返回。
func (b *Bot) Run(ctx context.Context) error {
	return b.client.Run(ctx, nil)
}

// Close 停止客户端。
func (b *Bot) Close() {
	_ = b.client.Close()
}

// LastReceivedAt 返回最近一次收到消息的时间（零值表示从未收到）。
// 供托盘 tooltip 做"长时间无消息"的静默失败自检。
func (b *Bot) LastReceivedAt() time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastReceived
}

// State 返回当前连接状态。
func (b *Bot) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

// Stats 返回累计收到的消息数与去重丢弃数。
func (b *Bot) Stats() (received, dropped int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.receivedCount, b.droppedCount
}

// msgKey 生成去重键。MessageID 缺失时退化为 序列+时间+发送者 的哈希。
func msgKey(msg *ilink.Message) int64 {
	if msg.MessageID != 0 {
		return msg.MessageID
	}
	h := fnv.New64a()
	fmt.Fprintf(h, "%d|%d|%s", msg.Seq, msg.CreateTimeMs, msg.FromUserID)
	return int64(h.Sum64())
}
