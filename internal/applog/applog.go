// Package applog 提供落盘日志：写入数据目录下的 airtype.log，
// 超过 2MB 时按写入量轮转为 airtype.old.log（单文件轮转，量级足够）。
//
// 任何文件问题都不阻断启动：主目录打不开就降级 temp 目录，
// 再不行退化为纯 stderr——常驻工具不能因为"日志文件被占用"而起不来。
package applog

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

const (
	logFile    = "airtype.log"
	oldLogFile = "airtype.old.log"
	maxBytes   = 2 << 20
)

// tolerantWriter 包装写入器，吞掉其错误（如 windowsgui 模式下的
// 无效 stderr 句柄）。io.MultiWriter 遇到首个 writer 报错会中断，
// 不吞掉的话文件也一个字都写不进——这正是 v1.1 首测时日志为空的原因。
type tolerantWriter struct{ w io.Writer }

func (t tolerantWriter) Write(p []byte) (int, error) {
	_, _ = t.w.Write(p)
	return len(p), nil
}

// rotatingWriter 落盘写入器：累计写入超过 maxBytes 即轮转。
// f == nil 表示完全降级（丢弃文件副本，stderr 副本仍保留）。
type rotatingWriter struct {
	mu  sync.Mutex
	dir string
	f   *os.File
	n   int64
}

// openLog 在 dir 打开日志文件；若已有日志超过 maxBytes 先轮转一次。
// 返回当前文件与已写入字节数。
func openLog(dir string) (*os.File, int64, error) {
	path := filepath.Join(dir, logFile)
	if fi, err := os.Stat(path); err == nil && fi.Size() >= maxBytes {
		_ = os.Remove(filepath.Join(dir, oldLogFile))
		_ = os.Rename(path, filepath.Join(dir, oldLogFile)) // 失败则继续追加
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, 0, err
	}
	var size int64
	if fi, err := f.Stat(); err == nil {
		size = fi.Size()
	}
	return f, size, nil
}

func (w *rotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return len(p), nil
	}
	if w.n+int64(len(p)) > maxBytes {
		w.rotateLocked()
	}
	n, err := w.f.Write(p)
	w.n += int64(n)
	if err != nil {
		w.f = nil // 写失败（如文件被删）：下次写入尝试重开
	}
	return n, err
}

// rotateLocked 轮转：关闭当前文件 → 改名为 old → 重开。改名失败
// （文件被编辑器/杀毒占用）则截断重开，优先保证日志继续可用。
func (w *rotatingWriter) rotateLocked() {
	_ = w.f.Close()
	w.f = nil
	dir := w.dir
	_ = os.Remove(filepath.Join(dir, oldLogFile))
	if err := os.Rename(filepath.Join(dir, logFile), filepath.Join(dir, oldLogFile)); err != nil {
		f, err2 := os.OpenFile(filepath.Join(dir, logFile), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err2 != nil {
			return
		}
		w.f, w.n = f, 0
		return
	}
	f, size, err := openLog(dir)
	if err != nil {
		return
	}
	w.f, w.n = f, size
}

// New 创建落盘 logger 与关闭函数。不再返回错误：日志不可用时逐级降级。
func New(dir string) (*slog.Logger, func()) {
	rw := openWriter(dir)
	w := io.MultiWriter(tolerantWriter{os.Stderr}, tolerantWriter{rw})
	logger := slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo}))
	return logger, func() {
		rw.mu.Lock()
		if rw.f != nil {
			_ = rw.f.Close()
			rw.f = nil
		}
		rw.mu.Unlock()
	}
}

// openWriter 依次尝试：数据目录 → temp 下的 AirType 目录 → 放弃文件（纯 stderr）。
func openWriter(dir string) *rotatingWriter {
	if err := os.MkdirAll(dir, 0o700); err == nil {
		if f, n, err := openLog(dir); err == nil {
			return &rotatingWriter{dir: dir, f: f, n: n}
		}
	}
	td := filepath.Join(os.TempDir(), "AirType")
	_ = os.MkdirAll(td, 0o700)
	if f, n, err := openLog(td); err == nil {
		return &rotatingWriter{dir: td, f: f, n: n}
	}
	return &rotatingWriter{dir: td}
}
