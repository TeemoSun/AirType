//go:build windows

package bot

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/the-yex/wechat-ilink-sdk/login"
)

func TestTokenStoreSealedRoundtrip(t *testing.T) {
	dir := t.TempDir()
	s, err := newDPAPITokenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	tok := &login.TokenInfo{Token: "secret-token", BaseURL: "https://x", UserID: "u1"}
	if err := s.Save("default", tok); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "default.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("secret-token")) {
		t.Fatal("token 不应明文落盘")
	}
	if !bytes.HasPrefix(data, []byte("DPAPIv1:")) {
		t.Fatalf("文件应为 DPAPI 密文: %q", data[:16])
	}
	got, err := s.Load("default")
	if err != nil || got == nil || got.Token != tok.Token {
		t.Fatalf("roundtrip 失败: %+v err=%v", got, err)
	}
}

func TestTokenStoreLegacyPlaintextMigration(t *testing.T) {
	dir := t.TempDir()
	plain := []byte(`{"token":"legacy-token","base_url":"https://y"}`)
	if err := os.WriteFile(filepath.Join(dir, "default.json"), plain, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := newDPAPITokenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Load("default")
	if err != nil || got == nil || got.Token != "legacy-token" {
		t.Fatalf("旧明文应可读: %+v err=%v", got, err)
	}
	// 读取后文件应已迁移为密文
	data, _ := os.ReadFile(filepath.Join(dir, "default.json"))
	if !bytes.HasPrefix(data, []byte("DPAPIv1:")) {
		t.Fatal("旧明文读取后应自动迁移为 DPAPI 加密")
	}
	// 迁移后再读仍一致
	got2, err := s.Load("default")
	if err != nil || got2 == nil || got2.Token != "legacy-token" {
		t.Fatalf("迁移后再读失败: %+v err=%v", got2, err)
	}
}

func TestTokenStoreGarbageTreatedAsEmpty(t *testing.T) {
	dir := t.TempDir()
	s, _ := newDPAPITokenStore(dir)
	// 半截密文（解不开）：按无 token 处理，走重新扫码
	if err := os.WriteFile(filepath.Join(dir, "default.json"),
		append([]byte("DPAPIv1:"), []byte("garbage")...), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load("default")
	if err != nil || got != nil {
		t.Fatalf("损坏凭据应视为无 token: %+v err=%v", got, err)
	}
	// 不存在的账号
	got2, err := s.Load("none")
	if err != nil || got2 != nil {
		t.Fatalf("不存在的账号应返回 nil: %+v err=%v", got2, err)
	}
}
