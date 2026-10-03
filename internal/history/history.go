// Package history 维护收到的文本消息历史：内存最新在前，
// JSON 落盘（防抖 500ms），容量上限 FIFO 裁剪（见方案 §4.4）。
package history

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Entry 是一条历史记录。
type Entry struct {
	ID         int64     `json:"id"`
	Text       string    `json:"text"`
	ReceivedAt time.Time `json:"received_at"`
}

// Store 是历史存储。所有方法线程安全。
type Store struct {
	mu       sync.Mutex
	entries  []Entry // 最新在前
	max      int
	path     string
	timer    *time.Timer
	delay    time.Duration
	closed   bool
	onChange func()

	// flushMu 串行化 Flush：防抖定时器、Close 与手动 Flush 可能并发，
	// 且必须拿到锁后重新序列化最新快照（而不是复用旧快照落盘）。
	flushMu sync.Mutex
}

// Open 打开（或创建）历史文件 path，容量上限 max 条。
func Open(path string, max int) (*Store, error) {
	s := &Store{max: max, path: path, delay: 500 * time.Millisecond}
	data, err := os.ReadFile(path)
	if err == nil && len(data) > 0 {
		_ = json.Unmarshal(data, &s.entries)
		if len(s.entries) > s.max {
			s.entries = s.entries[:s.max]
		}
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return s, nil
}

// OnChange 注册变更回调（用于 UI 刷新），须在 Open 后立即调用。
func (s *Store) OnChange(fn func()) {
	s.mu.Lock()
	s.onChange = fn
	s.mu.Unlock()
}

func (s *Store) notifyChanged() {
	if s.onChange != nil {
		go s.onChange()
	}
}

// Add 追加一条记录（最新在前），超容量裁掉最旧的，防抖落盘。
func (s *Store) Add(text string) Entry {
	e := Entry{ID: time.Now().UnixNano(), Text: text, ReceivedAt: time.Now()}
	s.mu.Lock()
	s.entries = append([]Entry{e}, s.entries...)
	if len(s.entries) > s.max {
		s.entries = s.entries[:s.max]
	}
	s.scheduleFlushLocked()
	s.mu.Unlock()
	s.notifyChanged()
	return e
}

// All 返回全部记录（最新在前）的副本。
func (s *Store) All() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Entry, len(s.entries))
	copy(out, s.entries)
	return out
}

// Delete 删除指定 id 的记录。
func (s *Store) Delete(id int64) {
	s.mu.Lock()
	for i, e := range s.entries {
		if e.ID == id {
			s.entries = append(s.entries[:i], s.entries[i+1:]...)
			break
		}
	}
	s.scheduleFlushLocked()
	s.mu.Unlock()
	s.notifyChanged()
}

// Clear 清空全部记录。
func (s *Store) Clear() {
	s.mu.Lock()
	s.entries = nil
	s.scheduleFlushLocked()
	s.mu.Unlock()
	s.notifyChanged()
}

// scheduleFlushLocked 重置防抖计时器（须持锁调用）。
func (s *Store) scheduleFlushLocked() {
	if s.closed {
		return
	}
	if s.timer != nil {
		s.timer.Stop()
	}
	s.timer = time.AfterFunc(s.delay, func() { _ = s.Flush() })
}

// Flush 立即落盘。临时文件名唯一（CreateTemp）并先 Sync 再改名，
// 避免并发落盘互相覆盖半截内容、以及断电留下空文件。
func (s *Store) Flush() error {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	s.mu.Lock()
	data, err := json.MarshalIndent(s.entries, "", " ")
	if err != nil {
		s.mu.Unlock()
		return err
	}
	path := s.path
	s.mu.Unlock()

	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// Close 停止防抖计时并最终落盘。
func (s *Store) Close() error {
	s.mu.Lock()
	s.closed = true
	if s.timer != nil {
		s.timer.Stop()
	}
	s.mu.Unlock()
	return s.Flush()
}
