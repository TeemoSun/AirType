package applog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewWritesToFile(t *testing.T) {
	dir := t.TempDir()
	logger, closer := New(dir)
	logger.Info("hello", "k", "v")
	closer()

	data, err := os.ReadFile(filepath.Join(dir, logFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "hello") {
		t.Fatalf("日志应包含 hello: %q", data)
	}
}

func TestRotationOnOversize(t *testing.T) {
	dir := t.TempDir()
	// 预置一个超限的旧日志
	if err := os.WriteFile(filepath.Join(dir, logFile), make([]byte, maxBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	logger, closer := New(dir)
	logger.Info("after-rotation")
	closer()

	if _, err := os.Stat(filepath.Join(dir, oldLogFile)); err != nil {
		t.Fatalf("超限日志应被轮转: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, logFile))
	if !strings.Contains(string(data), "after-rotation") {
		t.Fatalf("新日志应从空文件开始: %q", data)
	}
}

// TestRotationDuringRuntime 常驻进程的按写入量轮转：写满后应改名 old 并继续可用。
func TestRotationDuringRuntime(t *testing.T) {
	dir := t.TempDir()
	logger, closer := New(dir)
	line := strings.Repeat("x", 1024)
	for i := 0; i < maxBytes/1024+10; i++ {
		logger.Info(line)
	}
	closer()

	if _, err := os.Stat(filepath.Join(dir, oldLogFile)); err != nil {
		t.Fatalf("写满后应产生 old 日志: %v", err)
	}
	cur, _ := os.ReadFile(filepath.Join(dir, logFile))
	if len(cur) > maxBytes+4096 {
		t.Fatalf("当前日志应回到小文件: %d bytes", len(cur))
	}
	old, _ := os.ReadFile(filepath.Join(dir, oldLogFile))
	if len(old) == 0 {
		t.Fatal("old 日志不应为空")
	}
}

// TestFallbackWhenDirUnavailable 目录不可用时降级到 temp，不返回错误也不崩溃。
func TestFallbackWhenDirUnavailable(t *testing.T) {
	// 用一个文件路径充当目录，MkdirAll 必然失败
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	logger, closer := New(blocker) // 不应 panic/报错
	logger.Info("fallback-test")
	closer()

	// 降级目录应收到日志
	data, err := os.ReadFile(filepath.Join(blocker, logFile))
	if err == nil && strings.Contains(string(data), "fallback-test") {
		t.Fatal("不应写进被阻塞的路径")
	}
	fb, err := os.ReadFile(filepath.Join(os.TempDir(), "AirType", logFile))
	if err != nil || !strings.Contains(string(fb), "fallback-test") {
		t.Fatalf("应降级写入 temp 目录: err=%v", err)
	}
}
