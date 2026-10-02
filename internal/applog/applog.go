// Package applog 提供落盘日志：写入数据目录下的 airtype.log，
// 超过 2MB 时轮转为 airtype.old.log（单文件轮转，量级足够）。
package applog

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

const (
	logFile    = "airtype.log"
	oldLogFile = "airtype.old.log"
	maxBytes   = 2 << 20
)

// New 打开（或轮转后新建）日志文件，返回 logger 与关闭函数。
// 控制台版同时输出到 stderr；windowsgui 版 stderr 无人消费，无害。
func New(dir string) (*slog.Logger, func(), error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("applog: 创建日志目录失败: %w", err)
	}
	path := filepath.Join(dir, logFile)

	if fi, err := os.Stat(path); err == nil && fi.Size() > maxBytes {
		_ = os.Remove(filepath.Join(dir, oldLogFile))
		if err := os.Rename(path, filepath.Join(dir, oldLogFile)); err != nil {
			return nil, nil, fmt.Errorf("applog: 轮转日志失败: %w", err)
		}
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("applog: 打开日志失败: %w", err)
	}

	logger := slog.New(slog.NewTextHandler(io.MultiWriter(os.Stderr, f), &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	closer := func() { _ = f.Close() }
	return logger, closer, nil
}
