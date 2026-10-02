package applog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewWritesToFile(t *testing.T) {
	dir := t.TempDir()
	logger, closer, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
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
	logger, closer, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
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
