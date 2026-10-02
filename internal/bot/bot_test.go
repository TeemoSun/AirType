package bot

import "testing"

func TestDeduperFirstSeen(t *testing.T) {
	d := newDeduper(4)
	for _, k := range []int64{1, 2, 3} {
		if !d.check(k) {
			t.Fatalf("key %d 首次出现应返回 true", k)
		}
	}
}

func TestDeduperDuplicate(t *testing.T) {
	d := newDeduper(4)
	d.check(1)
	d.check(2)
	if d.check(1) {
		t.Fatal("重复 key 应返回 false")
	}
	if d.dropped != 1 {
		t.Fatalf("dropped = %d, want 1", d.dropped)
	}
}

func TestDeduperRingEviction(t *testing.T) {
	d := newDeduper(3)
	for _, k := range []int64{1, 2, 3} {
		d.check(k)
	}
	if !d.check(4) { // 淘汰 1，4 首次出现
		t.Fatal("key 4 应首次出现")
	}
	if !d.check(1) { // 1 已被淘汰出窗口，环形语义下重新视为新消息
		t.Fatal("key 1 已被淘汰出窗口，应重新视为新消息")
	}
}

func TestDeduperEvictionActuallyEvicts(t *testing.T) {
	d := newDeduper(2)
	d.check(1)
	d.check(2)
	d.check(3) // 淘汰 1
	if len(d.seen) != 2 {
		t.Fatalf("seen 大小 = %d, want 2", len(d.seen))
	}
	if _, ok := d.seen[3]; !ok {
		t.Fatal("key 3 应在 seen 中")
	}
}
