package qqbot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

// QQ 机器人 WebSocket 网关协议（Discord 风格 op 码）。
const (
	opDispatch     = 0  // 服务端推送事件
	opHeartbeat    = 1  // 客户端心跳
	opIdentify     = 2  // 鉴权
	opResume       = 6  // 恢复会话（避免断线窗口丢事件）
	opReconnect    = 7  // 服务端要求重连
	opInvalidSess  = 9  // 会话无效（d 为 bool：是否可 RESUME）
	opHello        = 10 // 连接建立，携带 heartbeat_interval
	opHeartbeatAck = 11

	// intentGroupAndC2C = 1<<25：群 @ 消息 + C2C 私聊消息（C2C_MESSAGE_CREATE）。
	intentGroupAndC2C = 1 << 25

	closeAuthFailed = 4004
)

// 网关会话的超时与稳定性参数。
const (
	// gwIdleTimeout 是"无服务端数据"的空闲超时：每成功读到一帧就续期。
	// 正常链路上心跳 ACK（间隔约 30-45s）会持续续期；链路死亡时才真正到期。
	gwIdleTimeout = 90 * time.Second
	// gwWriteTimeout 单次写（心跳）的超时。
	gwWriteTimeout = 10 * time.Second
	// stableSession 会话存活超过此时长后断开，视为"稳定过"，重连退避复位。
	stableSession = 60 * time.Second
)

type wsPayload struct {
	Op int             `json:"op"`
	S  *int64          `json:"s,omitempty"`
	T  string          `json:"t,omitempty"`
	D  json.RawMessage `json:"d,omitempty"`
}

type helloData struct {
	HeartbeatIntervalMs int `json:"heartbeat_interval"`
}

type identifyData struct {
	Token   string `json:"token"`
	Intents int    `json:"intents"`
	Shard   [2]int `json:"shard"`
}

type resumeData struct {
	Token     string `json:"token"`
	SessionID string `json:"session_id"`
	Seq       int64  `json:"seq"`
}

type readyData struct {
	SessionID string `json:"session_id"`
	User      struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	} `json:"user"`
}

// c2cMessageEvent 是 C2C_MESSAGE_CREATE 的负载（QQ 侧字段名与微信不同）。
type c2cMessageEvent struct {
	ID        string `json:"id"`
	Content   string `json:"content"`
	Timestamp string `json:"timestamp"`
	Author    struct {
		UserOpenid string `json:"user_openid"`
	} `json:"author"`
}

// resumeState 保存上一条网关会话的可恢复状态。只在 Run 的串行重连循环
// （含其调用的 runGateway/handleDispatch，均在同一 goroutine）里读写。
type resumeState struct {
	sessionID string
	seq       int64
}

// gatewayExit 描述一次网关会话退出原因，供外层决定重连策略。
type gatewayExit struct {
	err        error
	authFailed bool          // 4004：token 失效，先刷新再重连
	uptime     time.Duration // 本条会话存活时长（Run 用于退避复位）
}

// runGateway 建立一条 WebSocket 会话并阻塞处理直到出错/断开/ctx 取消。
// 若 Bot 上保存有 resume 状态则优先 RESUME（免 IDENTIFY、补发断线窗口事件）。
func (b *Bot) runGateway(ctx context.Context) gatewayExit {
	started := time.Now()
	tok, err := b.tokens.get(ctx)
	if err != nil {
		return gatewayExit{err: fmt.Errorf("qqbot: 获取 access_token 失败: %w", err)}
	}
	wssURL, err := fetchGatewayURL(ctx, tok)
	if err != nil {
		return gatewayExit{err: err}
	}

	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	conn, _, err := dialer.DialContext(ctx, wssURL, http.Header{})
	if err != nil {
		return gatewayExit{err: fmt.Errorf("qqbot: WebSocket 连接失败: %w", err)}
	}
	defer conn.Close()

	var hello wsPayload
	if err := readPayload(conn, &hello); err != nil || hello.Op != opHello {
		return gatewayExit{err: fmt.Errorf("qqbot: 等待 HELLO 失败: op=%d err=%v", hello.Op, err)}
	}
	var hd helloData
	if err := json.Unmarshal(hello.D, &hd); err != nil || hd.HeartbeatIntervalMs <= 0 {
		return gatewayExit{err: fmt.Errorf("qqbot: HELLO 负载异常: %v", err)}
	}
	if rs := b.resume; rs != nil {
		if err := writePayload(conn, wsPayload{Op: opResume, D: mustJSON(resumeData{
			Token:     "QQBot " + tok,
			SessionID: rs.sessionID,
			Seq:       rs.seq,
		})}); err != nil {
			return gatewayExit{err: fmt.Errorf("qqbot: 发送 RESUME 失败: %w", err)}
		}
	} else if err := writePayload(conn, wsPayload{Op: opIdentify, D: mustJSON(identifyData{
		Token:   "QQBot " + tok,
		Intents: intentGroupAndC2C,
		Shard:   [2]int{0, 1},
	})}); err != nil {
		return gatewayExit{err: fmt.Errorf("qqbot: 发送 IDENTIFY 失败: %w", err)}
	}

	// 心跳 goroutine：固定间隔发 op1，d 携带最后事件序号。
	// seqCh 是会话局部的：避免跨会话串号。
	hbCtx, hbCancel := context.WithCancel(ctx)
	defer hbCancel()
	seqCh := make(chan int64, 1)
	go func() {
		ticker := time.NewTicker(time.Duration(hd.HeartbeatIntervalMs) * time.Millisecond)
		defer ticker.Stop()
		var seq int64
		for {
			select {
			case <-hbCtx.Done():
				return
			case s := <-seqCh:
				seq = s
			case <-ticker.C:
				d := seq
				_ = conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
				if err := writePayload(conn, wsPayload{Op: opHeartbeat, S: &d}); err != nil {
					conn.Close() // 写失败：让读端尽快退出
					return
				}
			}
		}
	}()

	for {
		if ctx.Err() != nil {
			return gatewayExit{err: ctx.Err(), uptime: time.Since(started)}
		}
		var p wsPayload
		if err := readPayload(conn, &p); err != nil {
			var cerr *websocket.CloseError
			if errors.As(err, &cerr) && cerr.Code == closeAuthFailed {
				b.tokens.invalidate()
				b.resume = nil // 换 token 后旧会话不再可信，走全新 IDENTIFY
				return gatewayExit{err: errors.New("qqbot: 网关拒绝 token (4004)"), authFailed: true, uptime: time.Since(started)}
			}
			if ctx.Err() != nil {
				return gatewayExit{err: ctx.Err(), uptime: time.Since(started)}
			}
			return gatewayExit{err: fmt.Errorf("qqbot: 网关连接断开: %w", err), uptime: time.Since(started)}
		}
		if p.Op == opDispatch && p.S != nil {
			select { // 非阻塞更新心跳序号
			case seqCh <- *p.S:
			default:
			}
			if b.resume != nil {
				b.resume.seq = *p.S
			}
		}
		switch p.Op {
		case opDispatch:
			b.handleDispatch(p)
		case opHeartbeatAck:
			// 正常
		case opReconnect:
			// 保留 resume：下一条连接直接 RESUME，补发断线窗口内的事件
			return gatewayExit{err: errors.New("qqbot: 服务端要求重连"), uptime: time.Since(started)}
		case opInvalidSess:
			// d 为 bool：true 可重试 RESUME，false 必须重新 IDENTIFY
			var canResume bool
			if err := json.Unmarshal(p.D, &canResume); err != nil || !canResume {
				b.resume = nil
			}
			return gatewayExit{err: errors.New("qqbot: 会话失效"), uptime: time.Since(started)}
		}
	}
}

// readPayload 读一帧并把读超时续期为"再等 gwIdleTimeout"。
// 这是空闲超时而非连接寿命：只要有服务端数据（含心跳 ACK）就永不到期。
func readPayload(conn *websocket.Conn, p *wsPayload) error {
	_ = conn.SetReadDeadline(time.Now().Add(gwIdleTimeout))
	return conn.ReadJSON(p)
}

func writePayload(conn *websocket.Conn, p wsPayload) error {
	_ = conn.SetWriteDeadline(time.Now().Add(gwWriteTimeout))
	return conn.WriteJSON(p)
}

// handleDispatch 处理 op0 事件；只关心 READY / RESUMED 与 C2C_MESSAGE_CREATE。
func (b *Bot) handleDispatch(p wsPayload) {
	switch p.T {
	case "READY":
		var rd readyData
		if err := json.Unmarshal(p.D, &rd); err == nil {
			b.resume = &resumeState{sessionID: rd.SessionID}
			b.opts.Logger.Info("QQ 网关就绪", "session", rd.SessionID, "bot", rd.User.Username)
		}
		b.setState(StateConnected)
	case "RESUMED":
		if b.resume != nil {
			b.opts.Logger.Info("QQ 网关会话已恢复", "session", b.resume.sessionID)
		}
		b.setState(StateConnected)
	case "C2C_MESSAGE_CREATE":
		var ev c2cMessageEvent
		if err := json.Unmarshal(p.D, &ev); err != nil {
			b.opts.Logger.Error("qqbot: C2C 事件解析失败", "err", err)
			return
		}
		b.onC2C(ev)
	case "C2C_MSG_REJECT":
		b.opts.Logger.Warn("qqbot: 用户关闭了机器人私聊（C2C_MSG_REJECT），已停止向其推送")
	}
}

// onC2C 处理一条私聊消息：来源过滤 → 去重 → 记录收信 → 回调上层注入。
func (b *Bot) onC2C(ev c2cMessageEvent) {
	if ev.ID == "" {
		return
	}
	if b.openid != "" && ev.Author.UserOpenid != b.openid {
		// 安全边界：只有绑定者本人能驱动注入（凭据里记录的 openid）
		b.opts.Logger.Warn("qqbot: 忽略非绑定者的消息", "sender", hashOpenid(ev.Author.UserOpenid))
		return
	}
	if !b.dedup.check("qq:" + ev.ID) {
		b.mu.Lock()
		b.dropped++
		b.mu.Unlock()
		return
	}
	b.mu.Lock()
	b.lastReceived = time.Now()
	b.received++
	b.mu.Unlock()
	content := normalizeContent(ev.Content)
	if content == "" {
		b.opts.Logger.Info("qqbot: 非文本消息已忽略（图片/语音等）", "id", ev.ID)
		return
	}
	b.opts.Logger.Info("qqbot: 收到 C2C 消息", "chars", len([]rune(content)), "sender", hashOpenid(ev.Author.UserOpenid))
	if b.opts.OnText != nil {
		if err := b.opts.OnText(content); err != nil {
			b.opts.Logger.Error("qqbot: 消息处理失败", "id", ev.ID, "err", err)
		}
	}
}

func fetchGatewayURL(ctx context.Context, token string) (string, error) {
	var resp struct {
		URL string `json:"url"`
	}
	ctx, cancel := context.WithTimeout(ctx, bindTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, gatewayAPIURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "QQBot "+token)
	httpResp, err := httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("qqbot: 获取网关地址失败: %w", err)
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("qqbot: 获取网关地址 HTTP %d", httpResp.StatusCode)
	}
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return "", err
	}
	if resp.URL == "" {
		return "", errors.New("qqbot: 网关地址为空")
	}
	return resp.URL, nil
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
