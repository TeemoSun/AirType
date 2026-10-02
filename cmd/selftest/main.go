//go:build windows

// selftest 验证注入链路：激活目标窗口（或等待当前前台窗口），
// 把给定文本注入进去。配合 typetest-target 使用可无人值守验证。
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/TeemoSun/AirType/internal/typer"
	"github.com/TeemoSun/AirType/internal/win"
)

func main() {
	text := flag.String("text", "hello 世界 \U0001F600", "要注入的文本")
	title := flag.String("window", "", "目标窗口标题（留空则注入当前前台窗口）")
	wait := flag.Duration("wait", 0, "注入前等待时长（留出切换焦点的时间）")
	flag.Parse()

	if *wait > 0 {
		time.Sleep(*wait)
	}

	if *title != "" {
		hwnd := win.ByTitle(*title)
		if hwnd == 0 {
			fmt.Fprintln(os.Stderr, "selftest: 找不到窗口:", *title)
			os.Exit(2)
		}
		if err := win.Activate(hwnd, 3*time.Second); err != nil {
			fmt.Fprintln(os.Stderr, "selftest:", err)
			os.Exit(3)
		}
	}

	if err := typer.Type(*text); err != nil {
		fmt.Fprintln(os.Stderr, "selftest:", err)
		os.Exit(1)
	}
	fmt.Printf("selftest: 已注入 %d 个字符（UTF-8 字节数 %d）\n", len([]rune(*text)), len(*text))
}
