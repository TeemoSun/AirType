package history

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func open(t *testing.T, max int) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "history.json")
	s, err := Open(path, max)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

func TestAddLatestFirst(t *testing.T) {
	s, _ := open(t, 10)
	s.Add("first")
	s.Add("second")
	all := s.All()
	if len(all) != 2 || all[0].Text != "second" || all[1].Text != "first" {
		t.Fatalf("应最新在前, got %+v", all)
	}
}

func TestFIFOTrim(t *testing.T) {
	s, _ := open(t, 3)
	for _, txt := range []string{"a", "b", "c", "d", "e"} {
		s.Add(txt)
	}
	all := s.All()
	if len(all) != 3 {
		t.Fatalf("容量应为 3, got %d", len(all))
	}
	if all[0].Text != "e" || all[2].Text != "c" {
		t.Fatalf("应保留最新 3 条 (e,d,c), got %+v", all)
	}
}

func TestDeleteAndClear(t *testing.T) {
	s, _ := open(t, 10)
	e1 := s.Add("keep")
	e2 := s.Add("drop")
	s.Delete(e2.ID)
	all := s.All()
	if len(all) != 1 || all[0].ID != e1.ID {
		t.Fatalf("删除失败: %+v", all)
	}
	s.Clear()
	if got := len(s.All()); got != 0 {
		t.Fatalf("清空失败: %d", got)
	}
}

func TestDebounceFlush(t *testing.T) {
	s, path := open(t, 10)
	s.Add("hello")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("防抖期内不应落盘")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("防抖超时后应落盘:", err)
	}
	if len(data) == 0 || !contains(data, "hello") {
		t.Fatalf("落盘内容异常: %q", data)
	}
}

func TestPersistenceRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	s1, err := Open(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	s1.Add("persisted 世界 😀")
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	all := s2.All()
	if len(all) != 1 || all[0].Text != "persisted 世界 😀" {
		t.Fatalf("重启后历史应保留: %+v", all)
	}
}

func contains(data []byte, sub string) bool {
	return string(data) != "" && len(sub) > 0 && stringContains(string(data), sub)
}

func stringContains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
