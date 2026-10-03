//go:build windows

package qqbot

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDecryptSecretRoundtrip(t *testing.T) {
	// 按 Node 端字节布局加密：iv(12) + ct + tag(16)，整体 base64
	key := make([]byte, 32)
	rand.Read(key)
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	want := "45uWv65rQlrjNxLW"
	sealed := gcm.Seal(nil, key[:12], []byte(want), nil) // ct||tag
	data := append(append([]byte{}, key[:12]...), sealed...)
	got, err := decryptSecret(base64.StdEncoding.EncodeToString(data),
		base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatalf("decryptSecret: %v", err)
	}
	if got != want {
		t.Fatalf("解密结果 %q, want %q", got, want)
	}
	if _, err := decryptSecret("!!!notbase64!!!", "AA=="); err == nil {
		t.Fatalf("坏密文应报错")
	}
}

func TestConnectURL(t *testing.T) {
	u := connectURL("task/中文")
	if !strings.Contains(u, "task%2F%E4%B8%AD%E6%96%87") {
		t.Fatalf("task_id 应被转义: %s", u)
	}
	if !strings.Contains(u, "source=openclaw") || !strings.Contains(u, "_wv=2") {
		t.Fatalf("URL 参数缺失: %s", u)
	}
}

// TestBindFlowExpireThenComplete 模拟：第一个二维码过期 → 换新任务 → 扫码完成。
func TestBindFlowExpireThenComplete(t *testing.T) {
	var taskCount atomic.Int64
	var encrypted atomic.Value
	mux := http.NewServeMux()
	var pollCount atomic.Int64
	mux.HandleFunc("/lite/create_bind_task", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Key string `json:"key"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		n := taskCount.Add(1)
		// 模拟服务端：用该任务的 key 加密 AppSecret
		k, _ := base64.StdEncoding.DecodeString(req.Key)
		block, _ := aes.NewCipher(k)
		gcm, _ := cipher.NewGCM(block)
		sealed := gcm.Seal(nil, k[:12], []byte("secret-xyz"), nil)
		encrypted.Store(base64.StdEncoding.EncodeToString(append(append([]byte{}, k[:12]...), sealed...)))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"retcode": 0,
			"data":    map[string]string{"task_id": "task-" + string(rune('A'+n-1))},
		})
	})
	mux.HandleFunc("/lite/poll_bind_result", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&struct{}{})
		n := pollCount.Add(1)
		status := 0
		if taskCount.Load() == 1 {
			if n >= 2 {
				status = 3 // 第一个任务：未扫 → 过期
			}
		} else {
			status = 2 // 第二个任务：完成
		}
		data := map[string]any{"status": status}
		if status == 2 {
			data["bot_appid"] = "1905715921"
			data["bot_encrypt_secret"] = encrypted.Load()
			data["user_openid"] = "openid-1"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"retcode": 0, "data": data})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	oldHost := bindHost
	bindHost = srv.URL
	defer func() { bindHost = oldHost }()

	sleepBindPoll = time.Millisecond
	defer func() { sleepBindPoll = 2 * time.Second }()

	b, err := New(Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	creds, err := b.Bind(ctx)
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if creds.AppID != "1905715921" || creds.AppSecret != "secret-xyz" {
		t.Fatalf("凭据不符: %+v", creds)
	}
	got, ok, err := LoadCreds(b.opts.DataDir)
	if err != nil || !ok || got.AppSecret != "secret-xyz" {
		t.Fatalf("凭据未持久化: ok=%v err=%v", ok, err)
	}
	if b.tokens == nil {
		t.Fatalf("绑定后应初始化 token 源")
	}
}

func TestC2CDispatchAndDedup(t *testing.T) {
	b, _ := New(Options{DataDir: t.TempDir()})
	var got []string
	b.opts.OnText = func(s string) error { got = append(got, s); return nil }

	raw := `{"id":"msg1","content":"  你好 QQ \n","timestamp":"2026-10-03T12:00:00+08:00","author":{"user_openid":"u1"}}`
	var ev c2cMessageEvent
	_ = json.Unmarshal([]byte(raw), &ev)
	b.onC2C(ev)
	b.onC2C(ev) // 重复应被去重
	if len(got) != 1 || got[0] != "你好 QQ" {
		t.Fatalf("C2C 回调异常: %q", got)
	}
	r, d := b.Stats()
	if r != 1 || d != 1 {
		t.Fatalf("计数异常 received=%d dropped=%d", r, d)
	}
	if b.LastReceivedAt().IsZero() {
		t.Fatalf("LastReceivedAt 应更新")
	}
}

func TestNormalizeContent(t *testing.T) {
	cases := map[string]string{
		"  hi  ":   "hi",
		"":         "",
		"   ":      "",
		"@bot 你好":  "你好",
		"@bot\n你好": "你好",
		"多行\n消息":   "多行\n消息",
	}
	for in, want := range cases {
		if got := normalizeContent(in); got != want {
			t.Errorf("normalizeContent(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCredsRoundtrip(t *testing.T) {
	dir := t.TempDir()
	if _, ok, err := LoadCreds(dir); err != nil || ok {
		t.Fatalf("初始应无凭据: ok=%v err=%v", ok, err)
	}
	c := Creds{AppID: "a1", AppSecret: "s1", UserOpenid: "u1"}
	if err := SaveCreds(dir, c); err != nil {
		t.Fatal(err)
	}
	got, ok, _ := LoadCreds(dir)
	if !ok || got != c {
		t.Fatalf("roundtrip 失败: %+v", got)
	}
	if err := ClearCreds(dir); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := LoadCreds(dir); ok {
		t.Fatalf("Clear 后应无凭据")
	}
}
