//go:build windows

// typetest-target 是注入自测的"靶子"窗口：一个多行文本框，
// 内容每次变化即写出到结果文件，供测试脚本比对注入结果。
package main

import (
	"log"
	"os"
	"path/filepath"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
)

// Title 是供 win.ByTitle 查找的窗口标题。
const Title = "AirType Typetest Target"

func main() {
	out := "typetest-result.txt"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}
	if abs, err := filepath.Abs(out); err == nil {
		out = abs
	}

	var te *walk.TextEdit
	var mw *walk.MainWindow
	_, err := MainWindow{
		AssignTo: &mw,
		Title:    Title,
		MinSize:  Size{Width: 520, Height: 320},
		Layout:   VBox{},
		Children: []Widget{
			TextEdit{
				AssignTo: &te,
				VScroll:  true,
				OnTextChanged: func() {
					_ = os.WriteFile(out, []byte(te.Text()), 0o644)
				},
			},
			Label{Text: "注入测试靶子：内容实时写入 " + out},
		},
	}.Run()
	if err != nil {
		log.Fatal(err)
	}
}
