//go:build windows

package ui

import (
	"bytes"
	"image/png"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
	"github.com/skip2/go-qrcode"

	"github.com/TeemoSun/AirType/internal/win"
)

// qrLogicalSize 是二维码的逻辑边长（96dpi 基准）。
const qrLogicalSize = 340

// ShowQR 显示微信扫码二维码窗口（线程安全）。imageURL 为二维码内容链接。
func (t *Tray) ShowQR(imageURL string) {
	t.ShowQRWindow("扫码绑定微信", "用手机微信扫描下方二维码，授权后自动连接", imageURL)
}

// ShowQRWindow 显示可自定义标题/提示的扫码窗口（QQ 机器人绑定等场景）。
// 线程安全。
func (t *Tray) ShowQRWindow(title, hint, imageURL string) {
	t.mw.Synchronize(func() {
		t.ensureQRWindow()
		if t.qrWin == nil {
			return
		}
		_ = t.qrWin.SetTitle("AirType · " + title)
		_ = t.qrTitleLbl.SetText(title)
		_ = t.qrHintLbl.SetText(hint)
		// 先 Show 让窗口落到具体显示器上，DPI() 才是真实值
		if !t.qrWin.Visible() {
			t.qrWin.Show()
		}
		dpi := t.qrWin.DPI()

		// 二维码按"物理像素"生成，再以 ForDPI 声明其逻辑尺寸：
		// 直接生成 340px 再 ForDPI 会被二次折算（首版实测只剩约一半大小）。
		px := qrLogicalSize * dpi / 96
		pngBytes, err := qrcode.Encode(imageURL, qrcode.Medium, px)
		if err != nil {
			t.cfg.Logger.Error("生成二维码失败", "err", err)
			return
		}
		img, err := png.Decode(bytes.NewReader(pngBytes))
		if err != nil {
			t.cfg.Logger.Error("解码二维码失败", "err", err)
			return
		}
		bmp, err := walk.NewBitmapFromImageForDPI(img, dpi)
		if err != nil {
			t.cfg.Logger.Error("创建二维码位图失败", "err", err)
			return
		}
		t.qrView.SetImage(bmp)
		t.qrShown = true
	})
}

// HideQR 关闭二维码窗口（登录成功后调用，线程安全）。
func (t *Tray) HideQR() {
	t.mw.Synchronize(func() {
		if t.qrWin != nil {
			t.qrWin.Hide()
		}
	})
}

// ensureQRWindow 惰性创建二维码窗口（必须在 UI 线程调用）。
// 标题与提示文案由 ShowQRWindow 每次刷新（微信/QQ 共用此窗口）。
// 注意：walk 窗口带子控件必须设 Layout，否则 WM_SIZE 时 startLayout 崩溃。
func (t *Tray) ensureQRWindow() {
	_, dark := currentPalette()
	if t.qrWin != nil {
		if t.qrDark == dark {
			return
		}
		t.qrWin.Dispose()
		t.qrWin = nil
	}
	pal, _ := currentPalette()

	var w *walk.MainWindow
	var iv *walk.ImageView
	var ttl, hnt *walk.Label
	err := MainWindow{
		AssignTo:   &w,
		Title:      "AirType · 扫码绑定",
		Size:       Size{Width: 420, Height: 520},
		Background: SolidColorBrush{Color: pal.Window},
		Layout:     VBox{Margins: Margins{Left: 24, Top: 28, Right: 24, Bottom: 24}, Spacing: 12},
		Children: []Widget{
			Label{
				AssignTo:  &ttl,
				Text:      "",
				Font:      Font{Family: "Segoe UI", PointSize: 15, Bold: true},
				TextColor: pal.Text,
			},
			Label{
				AssignTo:  &hnt,
				Text:      "",
				TextColor: pal.TextSecondary,
			},
			// 二维码居中 + Zoom 模式：按控件可用空间等比缩放，
			// 不再依赖 ImageView 的 Ideal 尺寸链路
			ImageView{
				AssignTo:      &iv,
				Mode:          ImageViewModeZoom,
				MinSize:       Size{Width: qrLogicalSize, Height: qrLogicalSize},
				StretchFactor: 1,
				Margin:        12,
			},
		},
	}.Create()
	if err != nil {
		t.cfg.Logger.Error("创建二维码窗口失败", "err", err)
		return
	}
	t.qrWin = w
	t.qrView = iv
	t.qrTitleLbl = ttl
	t.qrHintLbl = hnt
	t.qrDark = dark
	win.FixWindowSize(uintptr(w.Handle()))
	// 点 ✕ 只隐藏不销毁；QQ 绑定流程据此取消
	w.Closing().Attach(func(canceled *bool, reason walk.CloseReason) {
		*canceled = true
		w.Hide()
		if t.cfg.QRDismissed != nil {
			go t.cfg.QRDismissed() // 回调里做通道切换（可能阻塞），不卡 UI 线程
		}
	})
	win.RoundCorners(uintptr(w.Handle()))
	win.EnableDarkTitlebar(uintptr(w.Handle()), dark)
}
