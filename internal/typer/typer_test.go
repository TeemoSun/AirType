//go:build windows

package typer

import (
	"testing"
	"unsafe"
)

func TestInputStructLayout(t *testing.T) {
	// x64/arm64 下 Win32 INPUT 为 40 字节（union 最大成员 MOUSEINPUT=32），
	// 布局错误会导致 SendInput 因 cbSize 不匹配整体拒绝
	if got := unsafe.Sizeof(input{}); got != 40 {
		t.Fatalf("sizeof(INPUT) = %d, want 40", got)
	}
}

func TestBuildInputsASCII(t *testing.T) {
	ev := BuildInputs("ab")
	if len(ev) != 4 {
		t.Fatalf("事件数 = %d, want 4 (每个字符 down+up)", len(ev))
	}
	for i, want := range []uint16{'a', 'a', 'b', 'b'} {
		if ev[i].ki.wVk != 0 || ev[i].ki.wScan != want {
			t.Errorf("ev[%d] = vk:%d scan:%q, want unicode scan %q", i, ev[i].ki.wVk, rune(ev[i].ki.wScan), rune(want))
		}
	}
	if ev[0].ki.dwFlags != keyEventFUnicode || ev[1].ki.dwFlags != keyEventFUnicode|keyEventFKeyUp {
		t.Errorf("flags = %d/%d, want UNICODE / UNICODE|KEYUP", ev[0].ki.dwFlags, ev[1].ki.dwFlags)
	}
}

func TestBuildInputsCJK(t *testing.T) {
	// "中" U+4E2D 在 BMP 内，一次 down+up
	ev := BuildInputs("中")
	if len(ev) != 2 || ev[0].ki.wScan != 0x4E2D || ev[1].ki.wScan != 0x4E2D {
		t.Fatalf("BMP 字符应注入 2 个事件且 scan=U+4E2D, got %d 个", len(ev))
	}
}

func TestBuildInputsSurrogatePair(t *testing.T) {
	// 😀 U+1F600 必须拆成高低代理对 D83D DE00，共 4 个事件
	ev := BuildInputs("\U0001F600")
	if len(ev) != 4 {
		t.Fatalf("事件数 = %d, want 4 (代理对各一对 down+up)", len(ev))
	}
	wantScan := []uint16{0xD83D, 0xD83D, 0xDE00, 0xDE00}
	wantUp := []bool{false, true, false, true}
	for i := range ev {
		if ev[i].ki.wScan != wantScan[i] {
			t.Errorf("ev[%d].scan = %X, want %X", i, ev[i].ki.wScan, wantScan[i])
		}
		isUp := ev[i].ki.dwFlags&keyEventFKeyUp != 0
		if isUp != wantUp[i] {
			t.Errorf("ev[%d] up=%v, want %v", i, isUp, wantUp[i])
		}
	}
}

func TestBuildInputsNewlineAndControls(t *testing.T) {
	ev := BuildInputs("a\r\nb\u0000c\u007Fd\re")
	// 期望: a, VK_RETURN(\r\n 折叠), b, c, d, e —— NUL/DEL/孤立 \r 被过滤
	if len(ev) != 12 {
		t.Fatalf("事件数 = %d, want 12", len(ev))
	}
	// a(0,1) return(2,3) b(4,5) c(6,7) d(8,9) e(10,11)
	if ev[2].ki.wVk != vkReturn || ev[3].ki.wVk != vkReturn {
		t.Fatalf("\\r\\n 应折叠为一次 VK_RETURN")
	}
	if ev[2].ki.wScan != 0 {
		t.Errorf("VK_RETURN 事件不应带 scan code")
	}
	letters := []rune{'a', 'b', 'c', 'd', 'e'}
	idx := 0
	for i := 0; i < len(ev); i += 2 {
		if ev[i].ki.wVk == vkReturn {
			continue
		}
		if ev[i].ki.wScan != uint16(letters[idx]) {
			t.Errorf("位置 %d: scan = %q, want %q", i, rune(ev[i].ki.wScan), letters[idx])
		}
		idx++
	}
}

func TestBuildInputsEmpty(t *testing.T) {
	if ev := BuildInputs(""); len(ev) != 0 {
		t.Fatalf("空串应无事件, got %d", len(ev))
	}
	if ev := BuildInputs("\x01\x02\x7f"); len(ev) != 0 {
		t.Fatalf("纯控制字符应全部过滤, got %d", len(ev))
	}
}

func TestChunkingSpansWholeText(t *testing.T) {
	// 700 个 ASCII 字符 = 1400 事件，应被分成 6 批（1400/256 = 5.47 → 6 批）
	b := make([]byte, 700)
	for i := range b {
		b[i] = 'x'
	}
	ev := BuildInputs(string(b))
	var batches int
	for start := 0; start < len(ev); start += chunkSize {
		end := min(start+chunkSize, len(ev))
		batches++
		_ = ev[start:end]
	}
	if len(ev) != 1400 {
		t.Fatalf("事件数 = %d, want 1400", len(ev))
	}
	if batches != 6 {
		t.Fatalf("批数 = %d, want 6", batches)
	}
}
