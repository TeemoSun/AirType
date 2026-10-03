package qqbot

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeGW 用 httptest 模拟 token 端点、gateway/bot 端点与 WS 网关。
// session 脚本按连接序号执行（session[0] 第一条连接、session[1] 第二条……）。
type fakeGW struct {
	srv     *httptest.Server
	mu      sync.Mutex
	scripts []func(c *websocket.Conn, t *testing.T)
	next    int
}

func newFakeGW(t *testing.T, scripts ...func(c *websocket.Conn, t *testing.T)) *fakeGW {
	t.Helper()
	f := &fakeGW{scripts: scripts}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "tok-1", "expires_in": "7200"})
	})
	mux.HandleFunc("/gateway", func(w http.ResponseWriter, r *http.Request) {
		ws := "ws" + strings.TrimPrefix(f.srv.URL, "http") + "/ws"
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"url": ws})
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		i := f.next
		f.next++
		f.mu.Unlock()
		if i >= len(f.scripts) {
			t.Errorf("超出脚本的第 %d 条连接", i+1)
			return
		}
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		f.scripts[i](c, t)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// install 把 fakeGW 挂到包级 URL 变量（测试结束恢复）。
func (f *fakeGW) install(t *testing.T) {
	t.Helper()
	oldToken, oldGW := tokenURL, gatewayAPIURL
	tokenURL = f.srv.URL + "/token"
	gatewayAPIURL = f.srv.URL + "/gateway"
	t.Cleanup(func() { tokenURL, gatewayAPIURL = oldToken, oldGW })
}

func wsSend(c *websocket.Conn, op int, s *int64, event string, d any) {
	raw, _ := json.Marshal(d)
	_ = c.WriteJSON(wsPayload{Op: op, S: s, T: event, D: raw})
}

func wsClose(c *websocket.Conn, code int, reason string) {
	_ = c.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(code, reason), time.Now().Add(time.Second))
	_ = c.Close()
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func seqPtr(i int64) *int64 { return &i }

// TestGatewayIdentifyAndC2C 覆盖：token 获取 → HELLO → IDENTIFY（校验参数）→
// READY → C2C 收信（含心跳序号）→ 4004 → authFailed 退出。
func TestGatewayIdentifyAndC2C(t *testing.T) {
	var got []string
	var states []State
	identifyCh := make(chan wsPayload, 1)

	f := newFakeGW(t, func(c *websocket.Conn, t *testing.T) {
		wsSend(c, opHello, nil, "", helloData{HeartbeatIntervalMs: 600000})
		var idp wsPayload
		if err := c.ReadJSON(&idp); err != nil {
			t.Errorf("读 IDENTIFY 失败: %v", err)
			return
		}
		identifyCh <- idp
		wsSend(c, opDispatch, seqPtr(1), "READY", readyData{SessionID: "s1"})
		wsSend(c, opDispatch, seqPtr(2), "C2C_MESSAGE_CREATE", map[string]any{
			"id": "m1", "content": " 你好 QQ ",
			"author": map[string]string{"user_openid": "openid-1"},
		})
		wsSend(c, opHeartbeatAck, nil, "", nil)
		time.Sleep(150 * time.Millisecond) // 留时间让客户端处理事件
		wsClose(c, closeAuthFailed, "auth failed")
	})
	f.install(t)

	b, err := New(Options{DataDir: t.TempDir(), Logger: discardLogger()})
	if err != nil {
		t.Fatal(err)
	}
	b.tokens = newTokenSource("app", "secret")
	b.opts.OnText = func(s string) error { got = append(got, s); return nil }
	b.opts.OnState = func(s State) { states = append(states, s) }

	exit := b.runGateway(context.Background())
	if !exit.authFailed {
		t.Fatalf("4004 关闭应标记 authFailed: %+v", exit)
	}
	if len(got) != 1 || got[0] != "你好 QQ" {
		t.Fatalf("C2C 文本不符: %q", got)
	}
	if len(states) == 0 || states[len(states)-1] != StateConnected {
		t.Fatalf("READY 后应 Connected: %v", states)
	}
	// IDENTIFY 参数校验
	var idp wsPayload
	select {
	case idp = <-identifyCh:
	default:
		t.Fatal("未捕获 IDENTIFY")
	}
	var id identifyData
	if err := json.Unmarshal(idp.D, &id); err != nil {
		t.Fatalf("IDENTIFY 负载: %v", err)
	}
	if id.Token != "QQBot tok-1" || id.Intents != intentGroupAndC2C || id.Shard != [2]int{0, 1} {
		t.Fatalf("IDENTIFY 参数不符: %+v", id)
	}
	// 4004 应清理 resume 并失效 token
	if b.resume != nil {
		t.Fatalf("4004 后应清空 resume")
	}
}

// TestGatewayResume 覆盖：首条会话 READY+收信后正常关闭 → resume 状态保存 →
// 第二条连接发 RESUME（校验 session_id/seq）→ RESUMED → 补收新事件。
func TestGatewayResume(t *testing.T) {
	var got []string
	resumeCh := make(chan resumeData, 1)

	f := newFakeGW(t,
		func(c *websocket.Conn, t *testing.T) { // 第一条：IDENTIFY 流程后服务端主动断开
			wsSend(c, opHello, nil, "", helloData{HeartbeatIntervalMs: 600000})
			var idp wsPayload
			_ = c.ReadJSON(&idp)
			if idp.Op != opIdentify {
				t.Errorf("首连应 IDENTIFY, got op=%d", idp.Op)
			}
			wsSend(c, opDispatch, seqPtr(5), "READY", readyData{SessionID: "s-resume"})
			wsSend(c, opDispatch, seqPtr(6), "C2C_MESSAGE_CREATE", map[string]any{
				"id": "m1", "content": "第一条",
				"author": map[string]string{"user_openid": "openid-1"},
			})
			time.Sleep(100 * time.Millisecond)
			wsClose(c, 1000, "bye")
		},
		func(c *websocket.Conn, t *testing.T) { // 第二条：应收到 RESUME
			wsSend(c, opHello, nil, "", helloData{HeartbeatIntervalMs: 600000})
			var rp wsPayload
			if err := c.ReadJSON(&rp); err != nil {
				t.Errorf("读 RESUME 失败: %v", err)
				return
			}
			if rp.Op != opResume {
				t.Errorf("重连应发 RESUME(op6), got op=%d", rp.Op)
			}
			var rd resumeData
			_ = json.Unmarshal(rp.D, &rd)
			resumeCh <- rd
			wsSend(c, opDispatch, nil, "RESUMED", nil)
			wsSend(c, opDispatch, seqPtr(7), "C2C_MESSAGE_CREATE", map[string]any{
				"id": "m2", "content": "补发的事件",
				"author": map[string]string{"user_openid": "openid-1"},
			})
			time.Sleep(100 * time.Millisecond)
			wsClose(c, 1000, "bye")
		})
	f.install(t)

	b, _ := New(Options{DataDir: t.TempDir(), Logger: discardLogger()})
	b.tokens = newTokenSource("app", "secret")
	b.opts.OnText = func(s string) error { got = append(got, s); return nil }

	exit1 := b.runGateway(context.Background())
	if exit1.err == nil {
		t.Fatal("第一条会话应因服务端关闭而退出")
	}
	if b.resume == nil || b.resume.sessionID != "s-resume" || b.resume.seq != 6 {
		t.Fatalf("resume 状态不符: %+v", b.resume)
	}
	if len(got) != 1 {
		t.Fatalf("第一条会话应收 1 条: %q", got)
	}

	exit2 := b.runGateway(context.Background())
	if exit2.err == nil {
		t.Fatal("第二条会话应退出")
	}
	select {
	case rd := <-resumeCh:
		if rd.SessionID != "s-resume" || rd.Seq != 6 || !strings.HasPrefix(rd.Token, "QQBot ") {
			t.Fatalf("RESUME 参数不符: %+v", rd)
		}
	default:
		t.Fatal("未捕获 RESUME")
	}
	if len(got) != 2 || got[1] != "补发的事件" {
		t.Fatalf("RESUME 后应补收事件: %q", got)
	}
}

// TestRunAuthFailTwice 覆盖：4004 → 刷新 token 重试一次 → 仍 4004 → SessionExpired 返回。
func TestRunAuthFailTwice(t *testing.T) {
	closes := 0
	f := newFakeGW(t,
		func(c *websocket.Conn, t *testing.T) {
			closes++
			wsSend(c, opHello, nil, "", helloData{HeartbeatIntervalMs: 600000})
			var p wsPayload
			_ = c.ReadJSON(&p)
			wsClose(c, closeAuthFailed, "x")
		},
		func(c *websocket.Conn, t *testing.T) {
			closes++
			wsSend(c, opHello, nil, "", helloData{HeartbeatIntervalMs: 600000})
			var p wsPayload
			_ = c.ReadJSON(&p)
			wsClose(c, closeAuthFailed, "x")
		},
	)
	f.install(t)

	b, _ := New(Options{DataDir: t.TempDir(), Logger: discardLogger()})
	b.tokens = newTokenSource("app", "secret")
	var lastState State
	b.opts.OnState = func(s State) { lastState = s }

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := b.Run(ctx)
	if err == nil {
		t.Fatal("二次 4004 应返回错误")
	}
	if lastState != StateSessionExpired {
		t.Fatalf("最终状态应 SessionExpired, got %v", lastState)
	}
	if closes != 2 {
		t.Fatalf("应恰好连接两次, got %d", closes)
	}
}

// TestOnC2COpenidFilter 覆盖：非绑定者的消息被忽略，不进 OnText。
func TestOnC2COpenidFilter(t *testing.T) {
	b, _ := New(Options{DataDir: t.TempDir(), Logger: discardLogger()})
	b.tokens = newTokenSource("app", "secret")
	b.openid = "owner"
	var got []string
	b.opts.OnText = func(s string) error { got = append(got, s); return nil }

	mk := func(id, openid string) c2cMessageEvent {
		ev := c2cMessageEvent{ID: id, Content: "hi"}
		ev.Author.UserOpenid = openid
		return ev
	}
	b.onC2C(mk("m1", "owner"))
	b.onC2C(mk("m2", "stranger"))
	if len(got) != 1 {
		t.Fatalf("只有绑定者的消息应回调: %q", got)
	}
	// 旧凭据（openid 未知）不拦截
	b.openid = ""
	b.onC2C(mk("m3", "stranger"))
	if len(got) != 2 {
		t.Fatalf("openid 未知时应放行: %q", got)
	}
}
