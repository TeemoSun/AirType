//go:build windows

// d2d.go：ID2D1HwndRenderTarget 直绘渲染器，供 Win11 材质窗口使用。
//
// 材质原理（本机 26300 实测，见开发方案 §10.7）：启用 DWM 系统材质 +
// 扩展框架后，窗口表面 alpha=0 的像素透出 Mica/Acrylic，非零 alpha 像素
// 正常显示。GDI 绘制不写 alpha（保持 0）→ 内容隐形；D2D 写 alpha → 可见。
// 因此材质窗口的**全部可见内容必须经本渲染器绘制**，背景区域保持不画。
//
// 槽位勘误（重要）：本机 d2d1.dll 的 ID2D1Factory vtable 顺序与 Wine/
// mingw 头不一致（按头文件 16 号调 CreateDCRenderTarget 返回 S_OK 但不写
// out，实际是两参数的几何创建器），CreateDCRenderTarget 已不可按文档槽位
// 定位，故渲染目标改走 HwndRenderTarget（14 号，E_HANDLE 特征指认 + 实测
// 成功）。ID2D1RenderTarget 本体的顺序与头文件一致且经视觉实证：
// CreateSolidColorBrush=8、FillRectangle=17、FillRoundedRectangle=19、
// DrawTextLayout=28、Clear=47、BeginDraw=48、EndDraw=49。
// IDWriteFactory 顺序与头文件一致（CreateTextFormat=15、CreateTextLayout=18
// 均实测）。
//
// COM 指针一律存 unsafe.Pointer；vtable 调用复用 dwrite.go 的 vtCall。

package win

import (
	"fmt"
	"sync"
	"syscall"
	"unsafe"
)

var iidD2DHwndRT = guid{0x2cd90694, 0x12e2, 0x11dc, [8]byte{0x9f, 0xed, 0x00, 0x11, 0x43, 0xa0, 0x55, 0xf9}}

var (
	pGetDpiForWindow = user32.NewProc("GetDpiForWindow")
	pGetClientRect   = user32.NewProc("GetClientRect")
)

// D2DAvailable 报告 D2D/DWrite 初始化是否可用（决定材质模式是否启用）。
func D2DAvailable() bool {
	if _, err := sharedD2DFactory(); err != nil {
		return false
	}
	_, err := sharedDWriteFactory()
	return err == nil
}

// clientRectPixels 取 hwnd 客户区物理像素尺寸。
func clientRectPixels(hwnd uintptr) (w, h int) {
	type rect struct{ l, t, r, b int32 }
	var rc rect
	pGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
	return int(rc.r - rc.l), int(rc.b - rc.t)
}

// ClientRectPixels 取 hwnd 客户区物理像素尺寸（供 UI 层创建渲染器用）。
func ClientRectPixels(hwnd uintptr) (w, h int) {
	return clientRectPixels(hwnd)
}

// D2DRenderer 绑定一个 HWND。非并发安全，仅 UI 线程使用。
type D2DRenderer struct {
	rt      unsafe.Pointer // ID2D1HwndRenderTarget
	dwf     unsafe.Pointer // IDWriteFactory
	w, h    float32        // 客户区逻辑尺寸（96dpi 单位）
	formats map[uint64]unsafe.Pointer
	brushes map[uint64]unsafe.Pointer
}

var (
	d2dFactoryOnce sync.Once
	d2dFactory     unsafe.Pointer
	d2dFactoryErr  error
)

func sharedD2DFactory() (unsafe.Pointer, error) {
	d2dFactoryOnce.Do(func() {
		coInit()
		var f unsafe.Pointer
		hr, _, _ := pD2D1CreateFactory.Call(0, uintptr(unsafe.Pointer(&iidD2DFactory)), 0,
			uintptr(unsafe.Pointer(&f)))
		if hr != 0 || f == nil {
			d2dFactoryErr = hrErr(hr, "D2D1CreateFactory")
			return
		}
		d2dFactory = f
	})
	return d2dFactory, d2dFactoryErr
}

var (
	dwriteFactoryOnce sync.Once
	dwriteFactory     unsafe.Pointer
	dwriteFactoryErr  error
)

func sharedDWriteFactory() (unsafe.Pointer, error) {
	dwriteFactoryOnce.Do(func() {
		coInit()
		var f unsafe.Pointer
		hr, _, _ := pDWriteCreateFactory.Call(0, uintptr(unsafe.Pointer(&iidDWFactory)),
			uintptr(unsafe.Pointer(&f)))
		if hr != 0 || f == nil {
			dwriteFactoryErr = hrErr(hr, "DWriteCreateFactory")
			return
		}
		dwriteFactory = f
	})
	return dwriteFactory, dwriteFactoryErr
}

// NewD2DRenderer 为 hwnd 创建直绘渲染目标（w/h 为客户区物理像素）。
func NewD2DRenderer(hwnd uintptr, w, h int) (*D2DRenderer, error) {
	factory, err := sharedD2DFactory()
	if err != nil {
		return nil, err
	}
	dwf, err := sharedDWriteFactory()
	if err != nil {
		return nil, err
	}
	// ID2D1HwndRenderTarget：props（96dpi=1DIP:1px，GDI 兼容）+ hwndProps
	props := rtProps{dxgiFormat: 87, alphaMode: 1, usage: 2}
	type hwndRtProps struct {
		hwnd           uintptr
		pixelW, pixelH uint32
		presentOptions uint32
	}
	hp := hwndRtProps{hwnd: hwnd, pixelW: uint32(w), pixelH: uint32(h)}
	var rt unsafe.Pointer
	hr, _, _ := vtCall(factory, 14, uintptr(unsafe.Pointer(&props)),
		uintptr(unsafe.Pointer(&hp)), uintptr(unsafe.Pointer(&rt)))
	if hr != 0 || rt == nil {
		return nil, hrErr(hr, "CreateHwndRenderTarget")
	}
	// 渲染目标 DPI 自动取桌面 DPI（props 传 0），绘制坐标为 DIP（96dpi
	// 逻辑单位），跨 DPI 显示器无需手动换算；这里存逻辑客户区尺寸供布局。
	dpi := uint32(96)
	if v, _, _ := pGetDpiForWindow.Call(hwnd); v != 0 {
		dpi = uint32(v)
	}
	scale := float32(dpi) / 96
	return &D2DRenderer{
		rt: rt, dwf: dwf,
		w:       float32(w) / scale,
		h:       float32(h) / scale,
		formats: map[uint64]unsafe.Pointer{},
		brushes: map[uint64]unsafe.Pointer{},
	}, nil
}

// Size 返回客户区逻辑尺寸（96dpi 单位；绘制坐标同该坐标系）。
func (r *D2DRenderer) Size() (w, h float32) { return r.w, r.h }

func (r *D2DRenderer) Dispose() {
	if r == nil {
		return
	}
	for _, f := range r.formats {
		release(f)
	}
	for _, b := range r.brushes {
		release(b)
	}
	if r.rt != nil {
		release(r.rt)
	}
	r.formats, r.brushes = nil, nil
	r.rt = nil
}

// brushFor 取/建 (COLORREF, alpha) 画刷。key = color | alpha<<32。
func (r *D2DRenderer) brushFor(color uint32, alpha float32) (unsafe.Pointer, error) {
	key := uint64(color) | uint64(*(*uint32)(unsafe.Pointer(&alpha)))<<32
	if b, ok := r.brushes[key]; ok {
		return b, nil
	}
	d2dColor := [4]float32{
		float32((color>>16)&0xff) / 255, // R（COLORREF 0x00BBGGRR）
		float32((color>>8)&0xff) / 255,  // G
		float32(color&0xff) / 255,       // B
		alpha,
	}
	var b unsafe.Pointer
	hr, _, _ := vtCall(r.rt, 8, uintptr(unsafe.Pointer(&d2dColor)), 0,
		uintptr(unsafe.Pointer(&b)))
	if hr != 0 || b == nil {
		return nil, hrErr(hr, "CreateSolidColorBrush")
	}
	r.brushes[key] = b
	return b, nil
}

// BeginDraw 开始一帧。绘制前不清屏：未绘制的像素保持 alpha=0，透出材质。
// BeginDraw 开始一帧。帧开头自动 ClearTransparent：不清屏则上一帧内容
// 残留叠加（窗口表面像素持续保留）；透明像素透出 DWM 材质。
func (r *D2DRenderer) BeginDraw() {
	vtCall(r.rt, 48)
	r.ClearTransparent()
}

// ClearTransparent 整面清为透明（alpha=0，槽位 47 Clear）。
func (r *D2DRenderer) ClearTransparent() {
	transparent := [4]float32{0, 0, 0, 0}
	vtCall(r.rt, 47, uintptr(unsafe.Pointer(&transparent)))
}

// EndDraw 结束一帧并呈现。
func (r *D2DRenderer) EndDraw() error {
	hr, _, _ := vtCall(r.rt, 49, 0, 0)
	if uint32(hr) != 0 {
		if uint32(hr) == 0x8899000C { // D2DERR_RECREATE_TARGET：作废缓存待重建
			for _, b := range r.brushes {
				release(b)
			}
			r.brushes = map[uint64]unsafe.Pointer{}
			return fmt.Errorf("win: D2D 渲染目标失效已重建缓存")
		}
		return hrErr(hr, "EndDraw")
	}
	return nil
}

// FillRect 填充矩形（color 为 COLORREF，alpha 0~1）。
func (r *D2DRenderer) FillRect(x, y, w, h float32, color uint32, alpha float32) {
	b, err := r.brushFor(color, alpha)
	if err != nil {
		return
	}
	rect := [4]float32{x, y, x + w, y + h}
	vtCall(r.rt, 17, uintptr(unsafe.Pointer(&rect)), uintptr(b)) // FillRectangle
}

// FillRoundedRect 填充圆角矩形。
func (r *D2DRenderer) FillRoundedRect(x, y, w, h, radius float32, color uint32, alpha float32) {
	b, err := r.brushFor(color, alpha)
	if err != nil {
		return
	}
	type d2dRRect struct {
		rect             [4]float32
		radiusX, radiusY float32
	}
	rr := d2dRRect{rect: [4]float32{x, y, x + w, y + h}, radiusX: radius, radiusY: radius}
	vtCall(r.rt, 19, uintptr(unsafe.Pointer(&rr)), uintptr(b)) // FillRoundedRectangle
}

// FillEllipse 填充圆/椭圆（行状态点等）。
func (r *D2DRenderer) FillEllipse(cx, cy, rx, ry float32, color uint32, alpha float32) {
	b, err := r.brushFor(color, alpha)
	if err != nil {
		return
	}
	type d2dEllipse struct {
		point    [2]float32
		radiusXY [2]float32
	}
	e := d2dEllipse{point: [2]float32{cx, cy}, radiusXY: [2]float32{rx, ry}}
	vtCall(r.rt, 21, uintptr(unsafe.Pointer(&e)), uintptr(b)) // FillEllipse
}

// DrawLine 画线段（关闭键 ✕ 等），width 为线宽像素。
// 注意：D2D1_POINT_2F 是 8 字节结构，x64 ABI 按**值**打包进一个寄存器
// （低 32 位 x、高 32 位 y），不是指针。
func (r *D2DRenderer) DrawLine(x1, y1, x2, y2, width float32, color uint32, alpha float32) {
	b, err := r.brushFor(color, alpha)
	if err != nil {
		return
	}
	p0 := packPoint(x1, y1)
	p1 := packPoint(x2, y2)
	vtCall(r.rt, 15, p0, p1,
		uintptr(b), uintptr(mathFloat32bits(width)), 0) // DrawLine
}

// textFormatKey：字号(pt)×10 + 粗体标志。
func textFormatKey(pt int, bold bool) uint64 {
	k := uint64(pt) * 10
	if bold {
		k++
	}
	return k
}

// textFormat 取/建 DWrite 文本格式（pt 为 96dpi 基准磅值，按 1DIP:1px 直接换算像素）。
func (r *D2DRenderer) textFormat(pt int, bold bool) (unsafe.Pointer, error) {
	key := textFormatKey(pt, bold)
	if f, ok := r.formats[key]; ok {
		return f, nil
	}
	family, _ := syscall.UTF16PtrFromString("Segoe UI Variable Text")
	if !IsWin11() {
		family, _ = syscall.UTF16PtrFromString("Segoe UI")
	}
	locale, _ := syscall.UTF16PtrFromString("")
	weight := uint32(400)
	if bold {
		weight = 700
	}
	var f unsafe.Pointer
	hr, _, _ := vtCall(r.dwf, 15, uintptr(unsafe.Pointer(family)), 0, uintptr(weight), 0, 5,
		uintptr(mathFloat32bits(float32(pt)*4.0/3.0)), uintptr(unsafe.Pointer(locale)),
		uintptr(unsafe.Pointer(&f)))
	if hr != 0 || f == nil {
		return nil, hrErr(hr, "CreateTextFormat")
	}
	r.formats[key] = f
	return f, nil
}

// DrawTextOpts 文本绘制选项。
type DrawTextOpts struct {
	Size       int // 磅值（96dpi 基准，如 10、8）
	Bold       bool
	Color      uint32  // COLORREF
	Alpha      float32 // 默认 1
	ColorEmoji bool    // 启用彩色字体（emoji）
	Ellipsis   bool    // 超宽省略号截断
	VCenter    bool    // 垂直居中
	HCenter    bool    // 水平居中
}

// DrawText 在矩形内绘制一行文本（坐标系为物理像素，1DIP=1px）。
func (r *D2DRenderer) DrawText(x, y, w, h float32, text string, opt DrawTextOpts) error {
	if text == "" {
		return nil
	}
	alpha := opt.Alpha
	if alpha == 0 {
		alpha = 1
	}
	f, err := r.textFormat(opt.Size, opt.Bold)
	if err != nil {
		return err
	}
	w16, err := syscall.UTF16FromString(text)
	if err != nil {
		return err
	}
	var layout unsafe.Pointer
	hr, _, _ := vtCall(r.dwf, 18, uintptr(unsafe.Pointer(&w16[0])),
		uintptr(len(w16)-1), uintptr(f),
		uintptr(mathFloat32bits(w)), uintptr(mathFloat32bits(h)),
		uintptr(unsafe.Pointer(&layout))) // CreateTextLayout
	if hr != 0 || layout == nil {
		return hrErr(hr, "CreateTextLayout")
	}
	defer release(layout)

	// IDWriteTextLayout：SetTextAlignment=3、SetParagraphAlignment=4、
	// SetWordWrapping=5、SetTrimming=9（TextFormat 槽位由 Layout 继承）
	if opt.HCenter {
		vtCall(layout, 3, 2) // TEXT_ALIGNMENT_CENTER
	}
	vtCall(layout, 5, 1) // NO_WRAP
	if opt.VCenter {
		vtCall(layout, 4, 2) // CENTER（段落垂直居中）
	}
	if opt.Ellipsis {
		var ell unsafe.Pointer
		hrE, _, _ := vtCall(r.dwf, 20, 0, uintptr(unsafe.Pointer(&ell))) // CreateEllipsisTrimmingSign
		if hrE == 0 && ell != nil {
			trim := [3]uint32{1, 0, 0} // CHARACTER
			vtCall(layout, 9, uintptr(unsafe.Pointer(&trim)), uintptr(ell))
		}
	}
	brush, err := r.brushFor(opt.Color, alpha)
	if err != nil {
		return err
	}
	opts := uintptr(0)
	if opt.ColorEmoji {
		opts = 4 // D2D1_DRAW_TEXT_OPTIONS_ENABLE_COLOR_FONT
	}
	// origin 是 D2D1_POINT_2F（8 字节），x64 ABI 按值打包进一个寄存器
	vtCall(r.rt, 28, packPoint(x, y), uintptr(layout),
		uintptr(brush), opts) // DrawTextLayout
	return nil
}

// packPoint 把 (x,y) 按 x64 ABI 打包成单个寄存器值（低字 x，高字 y）。
func packPoint(x, y float32) uintptr {
	return uintptr(mathFloat32bits(x)) | uintptr(mathFloat32bits(y))<<32
}
