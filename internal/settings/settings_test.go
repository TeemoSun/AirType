package settings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingReturnsZero(t *testing.T) {
	dir := t.TempDir()
	s, err := Load(dir)
	if err != nil {
		t.Fatalf("缺失文件应返回零值无错: %v", err)
	}
	if s.AutoEnter {
		t.Fatalf("默认 AutoEnter 应为 false")
	}
}

func TestSaveLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	want := Settings{AutoEnter: true}
	if err := Save(dir, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != want {
		t.Fatalf("roundtrip = %+v, want %+v", got, want)
	}
	// 覆盖写回 false
	if err := Save(dir, Settings{AutoEnter: false}); err != nil {
		t.Fatalf("覆盖 Save: %v", err)
	}
	got, err = Load(dir)
	if err != nil || got.AutoEnter {
		t.Fatalf("覆盖后应读到 false, got %+v err=%v", got, err)
	}
	// 原子写不应残留临时文件
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if e.Name() != FileName {
			t.Errorf("残留临时文件: %s", e.Name())
		}
	}
}

func TestLoadCorruptedReturnsZeroAndError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load(dir)
	if err == nil {
		t.Fatalf("损坏文件应返回错误")
	}
	if s.AutoEnter {
		t.Fatalf("损坏文件应返回零值设置")
	}
}
