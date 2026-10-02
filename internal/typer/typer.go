//go:build windows

// Package typer 把文本转换为 Win32 SendInput 键盘事件，注入当前前台窗口。
//
// 注入走 KEYEVENTF_UNICODE（按 UTF-16 码元），中文、全角标点、emoji 直接注入，
// 不依赖目标机器的输入法状态；\n 映射为一次 VK_RETURN。
package typer

import (
	"fmt"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

const (
	inputKeyboard    = 1
	keyEventFKeyUp   = 0x0002
	keyEventFUnicode = 0x0004
	vkReturn         = 0x0D

	// chunkSize 是单次 SendInput 调用的事件数上限，超长文本分批注入，
	// 避免一次提交过大的数组（上限行为未文档化，防御性分块）。
	chunkSize = 256
)

// keybdInput 对应 Win32 KEYBDINPUT。
type keybdInput struct {
	wVk         uint16
	wScan       uint16
	dwFlags     uint32
	time        uint32
	dwExtraInfo uintptr
}

// input 对应 Win32 INPUT。union 以最大成员 MOUSEINPUT 计：
// x64/arm64 下 MOUSEINPUT=32、KEYBDINPUT=24，故 sizeof(INPUT)=40，
// 键盘分支需补齐到同等大小，否则 SendInput 因 cbSize 不匹配而整体失败。
type input struct {
	typ uint32
	_   uint32 // union 8 字节对齐前的填充
	ki  keybdInput
	_   [8]byte // KEYBDINPUT(24) 补齐到 MOUSEINPUT(32)
}

var procSendInput = syscall.NewLazyDLL("user32.dll").NewProc("SendInput")

// keyTap 生成一次完整的按键（down + up）。downFlags 为按下事件的附加标志。
func keyTap(vk, scan uint16, downFlags uint32) []input {
	return []input{
		{typ: inputKeyboard, ki: keybdInput{wVk: vk, wScan: scan, dwFlags: downFlags}},
		{typ: inputKeyboard, ki: keybdInput{wVk: vk, wScan: scan, dwFlags: downFlags | keyEventFKeyUp}},
	}
}

// BuildInputs 把文本展开为键盘事件序列：
//   - "\r\n" 与 "\n" 折叠为一次 VK_RETURN（孤立 "\r" 丢弃）
//   - 其余 < 0x20 的控制字符与 0x7F 过滤
//   - 可见字符按 UTF-16 码元逐个注入，> U+FFFF（emoji 等）自动拆为高低代理对
func BuildInputs(s string) []input {
	var out []input
	for _, r := range s {
		switch {
		case r == '\r':
			// 与后续 \n 合并，孤立 \r 直接丢弃
		case r == '\n':
			out = append(out, keyTap(vkReturn, 0, 0)...)
		case r < 0x20 || r == 0x7F:
			// 过滤其余控制字符
		default:
			for _, u := range utf16.Encode([]rune{r}) {
				out = append(out, keyTap(0, u, keyEventFUnicode)...)
			}
		}
	}
	return out
}

// Type 将整段文本注入当前前台窗口，分批调用 SendInput。
// 任一批未完整注入（如被 UIPI 阻止）即返回错误。
func Type(s string) error {
	return sendEvents(BuildInputs(s))
}

// EnterInputs 生成一次完整回车（VK_RETURN down + up）的事件序列。
// 与 BuildInputs 对 "\n" 的折叠使用同一编码，行为一致。
func EnterInputs() []input {
	return keyTap(vkReturn, 0, 0)
}

// PressEnter 向前台窗口注入一次回车（"自动回车"发送功能）。
func PressEnter() error {
	return sendEvents(EnterInputs())
}

// sendEvents 分批调用 SendInput，任一批未完整注入（如被 UIPI 阻止）即返回错误。
func sendEvents(events []input) error {
	for start := 0; start < len(events); start += chunkSize {
		end := start + chunkSize
		if end > len(events) {
			end = len(events)
		}
		chunk := events[start:end]
		n, lastErr, _ := procSendInput.Call(
			uintptr(len(chunk)),
			uintptr(unsafe.Pointer(&chunk[0])),
			unsafe.Sizeof(input{}),
		)
		if n != uintptr(len(chunk)) {
			return fmt.Errorf("sendinput: 仅注入 %d/%d 个事件，last error %d（前台窗口可能拒绝了输入）",
				n, len(chunk), lastErr)
		}
	}
	return nil
}
