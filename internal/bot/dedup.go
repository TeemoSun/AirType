package bot

// deduper 记录最近 N 个消息键：重复出现返回 false，环形淘汰最旧的键。
type deduper struct {
	capacity int
	ring     []int64
	pos      int // 下一个被淘汰的位置（环形写满后）
	seen     map[int64]struct{}
	dropped  int
}

func newDeduper(capacity int) *deduper {
	return &deduper{
		capacity: capacity,
		seen:     make(map[int64]struct{}, capacity),
	}
}

// check 返回 key 是否首次出现；重复的 key 计入 dropped。
func (d *deduper) check(key int64) bool {
	if _, ok := d.seen[key]; ok {
		d.dropped++
		return false
	}
	d.seen[key] = struct{}{}
	if len(d.ring) < d.capacity {
		d.ring = append(d.ring, key)
		return true
	}
	delete(d.seen, d.ring[d.pos])
	d.ring[d.pos] = key
	d.pos++
	if d.pos >= d.capacity {
		d.pos = 0
	}
	return true
}
