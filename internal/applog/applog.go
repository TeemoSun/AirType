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

// tolerantWriter 包装一个写入器，吞掉其错误（如 windowsgui 模式下的
// 无效 stderr 句柄）。io.MultiWriter 遇到首个 writer 报错会中断，
// 不吞掉的话文件也一个字都写不进——这正是 v1.1 首测时日志为空的原因。
type tolerantWriter struct{ w io.Writer }

func (t tolerantWriter) Write(p []byte) (int, error) {
	_, _ = t.w.Write(p)
	return len(p), nil
}

// New 打开（或轮转后新建）日志文件，返回 logger 与关闭函数。
// 同时输出到 stderr（有控制台时可见；windowsgui 下自动无效但无害）。
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

	logger := slog.New(slog.NewTextHandler(io.MultiWriter(tolerantWriter{os.Stderr}, f), &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	closer := func() { _ = f.Close() }
	return logger, closer, nil
}
