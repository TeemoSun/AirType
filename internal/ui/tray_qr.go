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
		// 材质呈现：文案与链接存状态，画布重绘（QR 矩阵已就位）
		if t.qrMat != nil {
			_ = t.qrWin.SetTitle("AirType · " + title)
			t.qrTitleStr, t.qrHintStr, t.qrURL = title, hint, imageURL
			t.qrMatrix = nil // 链接可能变化，下次绘制时重生成
			if !t.qrWin.Visible() {
				t.qrWin.Show()
			}
			t.qrShown = true
			t.qrMat.redraw()
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
// 窗口为 Win11 风格无边框圆角浮层：无系统标题栏，头部行自带 ✕，
// 整面可拖动（子类化 NCHITTEST 返回 HTCAPTION）。
func (t *Tray) ensureQRWindow() {
	_, dark := currentPalette()
	if t.qrWin != nil {
		if t.qrDark == dark {
			return
		}
		t.qrWin.Dispose()
		t.qrWin = nil
		t.qrMat = nil
	}
	if materialOK() && t.ensureQRWindowMaterial(dark) {
		return
	}
	pal, _ := currentPalette()

	var w *walk.MainWindow
	var iv *walk.ImageView
	var ttl, hnt *walk.Label
	var hostClose *walk.Composite
	err := MainWindow{
		AssignTo:   &w,
		Title:      "AirType · 扫码绑定",
		Size:       Size{Width: 420, Height: 520},
		Background: SolidColorBrush{Color: pal.Window},
		Layout:     VBox{Margins: Margins{Left: 24, Top: 18, Right: 20, Bottom: 24}, Spacing: 12},
		Children: []Widget{
			// 头部行：标题 + 关闭键（MaxSize 钳高，理由同历史弹窗头部）
			Composite{
				Layout:  HBox{MarginsZero: true, Spacing: 8},
				MaxSize: Size{Height: 34},
				Children: []Widget{
					Label{
						AssignTo:  &ttl,
						Text:      "",
						Font:      Font{Family: DisplayFontFamily(), PointSize: 12, Bold: true},
						TextColor: pal.Text,
					},
					HSpacer{},
					Composite{
						AssignTo: &hostClose,
						Layout:   HBox{MarginsZero: true, SpacingZero: true},
						MinSize:  Size{Width: 34, Height: 28},
						MaxSize:  Size{Width: 34, Height: 28},
					},
				},
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
	// 无边框 + 强制圆角（Win11 风格浮层）
	win.MakeBorderlessRoundedPopup(uintptr(w.Handle()))
	// 整面可拖动：客户区全部按标题栏命中（子控件是独立 HWND，不受影响，
	// ✕ 与二维码区域照常接收鼠标）
	_ = win.SubclassWindow(uintptr(w.Handle()), func(msg uint32, wp, lp uintptr) (bool, uintptr) {
		if msg == 0x0084 { // WM_NCHITTEST
			return true, 2 // HTCAPTION
		}
		return false, 0
	})
	if _, err := newCloseButton(hostClose, func() { w.Close() }); err != nil {
		t.cfg.Logger.Error("创建关闭键失败", "err", err)
	}
	// 点 ✕ 只隐藏不销毁；QQ 绑定流程据此取消
	w.Closing().Attach(func(canceled *bool, reason walk.CloseReason) {
		*canceled = true
		w.Hide()
		if t.cfg.QRDismissed != nil {
			go t.cfg.QRDismissed() // 回调里做通道切换（可能阻塞），不卡 UI 线程
		}
	})
}
