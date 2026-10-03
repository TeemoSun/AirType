//go:build windows

package qqbot

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// State 与 bot.State 语义对齐，供托盘合并显示。
type State int

const (
	StateInit State = iota
	StateConnected
	StateSessionExpired // 凭据失效（如平台侧删除机器人），需重新扫码绑定
	StateDisconnected   // 网络断开，自动重连中
)

func (s State) String() string {
	switch s {
	case StateConnected:
		return "QQ已连接"
	case StateSessionExpired:
		return "QQ凭据失效"
	case StateDisconnected:
		return "QQ连接断开"
	default:
		return "QQ未启动"
	}
}

// ErrNotBound 表示尚未扫码绑定（无凭据）。
var ErrNotBound = errors.New("qqbot: 尚未绑定 QQ 机器人")

var errEmptyToken = errors.New("qqbot: access_token 为空")

// Options 是 QQ 机器人运行依赖。
type Options struct {
	DataDir string
	Logger  *slog.Logger

	// OnText 收到去重后的私聊文本时回调（与微信通道共用注入链路）。
	OnText func(text string) error
	// OnQRCode 扫码绑定时回调（参数为二维码内容 URL）。
	OnQRCode func(url string)
	// OnState 状态变化回调。
	OnState func(State)
	// OnBound 扫码绑定成功回调（凭据已持久化）。
	OnBound func(appID string)
}

// Bot 管理扫码绑定与 WebSocket 会话生命周期。
type Bot struct {
	opts   Options
	tokens *tokenSource
	dedup  *deduper
	seqCh  chan int64 // 心跳协程读取最新事件序号

	mu           sync.Mutex
	state        State
	lastReceived time.Time
	received     int
	dropped      int
}

// New 创建 Bot；凭据存在则立即准备 token 源。
func New(opts Options) (*Bot, error) {
	if opts.DataDir == "" {
		return nil, errors.New("qqbot: DataDir 不能为空")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	b := &Bot{opts: opts, dedup: newDeduper(500), seqCh: make(chan int64, 1)}
	if creds, ok, err := LoadCreds(opts.DataDir); err != nil {
		return nil, fmt.Errorf("qqbot: 读取凭据失败: %w", err)
	} else if ok {
		b.tokens = newTokenSource(creds.AppID, creds.AppSecret)
	}
	return b, nil
}

func httpClient() *http.Client {
	return &http.Client{Timeout: bindTimeout}
}

// sleepBindPoll 是绑定状态轮询间隔（测试可缩短）。
var sleepBindPoll = 2 * time.Second

// Bind 走扫码绑定流程：申请任务 → 回调展示二维码 → 轮询至确认 →
// 解密凭据并持久化。阻塞直至绑定成功或 ctx 取消。
func (b *Bot) Bind(ctx context.Context) (Creds, error) {
	client := httpClient()
	for {
		task, err := createBindTask(ctx, client)
		if err != nil {
			return Creds{}, err
		}
		if b.opts.OnQRCode != nil {
			b.opts.OnQRCode(connectURL(task.taskID))
		}
		b.opts.Logger.Info("qqbot: 等待扫码绑定", "task", task.taskID)

		for {
			if ctx.Err() != nil {
				return Creds{}, ctx.Err()
			}
			resp, err := pollBindResult(ctx, client, task.taskID)
			if err != nil {
				b.opts.Logger.Warn("qqbot: 轮询绑定状态失败，稍后重试", "err", err)
				time.Sleep(sleepBindPoll)
				continue
			}
			switch bindStatus(resp.Data.Status) {
			case bindCompleted:
				secret, err := decryptSecret(resp.Data.BotEncryptSecrt, task.key)
				if err != nil {
					return Creds{}, err
				}
				creds := Creds{
					AppID:      resp.Data.BotAppID,
					AppSecret:  secret,
					UserOpenid: resp.Data.UserOpenid,
				}
				if creds.AppID == "" {
					return Creds{}, errors.New("qqbot: 绑定完成但缺少 bot_appid")
				}
				if err := SaveCreds(b.opts.DataDir, creds); err != nil {
					return Creds{}, fmt.Errorf("qqbot: 保存凭据失败: %w", err)
				}
				b.tokens = newTokenSource(creds.AppID, creds.AppSecret)
				if b.opts.OnBound != nil {
					b.opts.OnBound(creds.AppID)
				}
				b.opts.Logger.Info("qqbot: 扫码绑定成功", "appId", creds.AppID)
				return creds, nil
			case bindExpired:
				b.opts.Logger.Info("qqbot: 二维码已过期，重新生成")
				goto nextTask
			default: // NONE / PENDING：继续轮询
				time.Sleep(sleepBindPoll)
			}
		}
	nextTask:
	}
}

// Bound 报告是否已有持久化凭据可用。
func (b *Bot) Bound() bool {
	return b.tokens != nil
}

// Run 阻塞运行：未绑定时返回 ErrNotBound；已绑定则维持网关会话，
// 断开后按指数退避重连（封顶 60s），直到 ctx 取消。
func (b *Bot) Run(ctx context.Context) error {
	if b.tokens == nil {
		return ErrNotBound
	}
	backoff := time.Second
	const maxBackoff = 60 * time.Second
	var lastAuthFail time.Time
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		exit := b.runGateway(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		b.setState(StateDisconnected)
		if exit.authFailed {
			// 4004 大多是 token 缓存与服务端失配；刷新后立即重试一次
			if time.Since(lastAuthFail) < time.Minute {
				b.opts.Logger.Error("qqbot: 刷新后仍 4004，凭据可能已在平台侧失效")
				b.setState(StateSessionExpired)
				return exit.err
			}
			lastAuthFail = time.Now()
			backoff = 2 * time.Second
			continue
		}
		b.opts.Logger.Warn("qqbot: 网关会话结束，准备重连", "err", exit.err, "backoff", backoff)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func (b *Bot) setState(s State) {
	b.mu.Lock()
	prev := b.state
	b.state = s
	b.mu.Unlock()
	if prev != s {
		b.opts.Logger.Info("QQ 状态变化", "from", prev.String(), "to", s.String())
		if b.opts.OnState != nil {
			b.opts.OnState(s)
		}
	}
}

// State 返回当前状态（线程安全）。
func (b *Bot) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

// LastReceivedAt 返回最近收信时间（零值=从未）。
func (b *Bot) LastReceivedAt() time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastReceived
}

// Stats 返回收信/去重丢弃计数。
func (b *Bot) Stats() (received, dropped int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.received, b.dropped
}

// normalizeContent 清理 C2C 文本：去首尾空白，剔除 @ 机器人残留前缀。
func normalizeContent(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// C2C 私聊一般无 @ 前缀，防御性剔除（@名 后以任意空白分隔）
	if strings.HasPrefix(s, "@") {
		if i := strings.IndexAny(s, " \t\n\r"); i >= 0 {
			s = strings.TrimSpace(s[i+1:])
		}
	}
	return s
}

// deduper 与 internal/bot 同构：环形淘汰的消息键去重器。
type deduper struct {
	capacity int
	ring     []int64
	pos      int
	seen     map[int64]struct{}
	dropped  int
}

func newDeduper(capacity int) *deduper {
	return &deduper{capacity: capacity, seen: make(map[int64]struct{}, capacity)}
}

func (d *deduper) check(key string) bool {
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	k := int64(h.Sum64())
	if _, ok := d.seen[k]; ok {
		d.dropped++
		return false
	}
	d.seen[k] = struct{}{}
	if len(d.ring) < d.capacity {
		d.ring = append(d.ring, k)
		return true
	}
	delete(d.seen, d.ring[d.pos])
	d.ring[d.pos] = k
	d.pos++
	if d.pos >= d.capacity {
		d.pos = 0
	}
	return true
}
