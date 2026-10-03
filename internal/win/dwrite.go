//go:build windows

// dwrite.go：Direct2D/DirectWrite 文本渲染。
//
// GDI 的 DrawText 不支持彩色字体（emoji 一律黑白轮廓），历史列表的
// 消息行走 D2D1 DC 渲染目标 + DWrite TextLayout：系统字体回退自动
// 启用 Segoe UI Emoji 的彩色字形。COM 指针一律存 unsafe.Pointer
// （vet 禁止 uintptr→unsafe.Pointer 转换）。
//
// COM vtable 槽位依据 Wine 的 d2d1.idl / dwrite.idl（与 Windows SDK 头
// 同源）：ID2D1Factory::CreateDCRenderTarget=16；ID2D1RenderTarget 的
// CreateSolidColorBrush=8、DrawTextLayout=28、Clear=47、BeginDraw=48、
// EndDraw=49；ID2D1DCRenderTarget::BindDC=57；IDWriteFactory 的
// CreateTextFormat=15、CreateTextLayout=18、CreateEllipsisTrimmingSign=20；
// IDWriteTextLayout 的 SetWordWrapping=5、SetParagraphAlignment=4、
// SetTrimming=9。初始化失败时返回错误，调用方回退 GDI。
package win

import (
	"fmt"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	d2d1DLL   = windows.NewLazySystemDLL("d2d1.dll")
	dwriteDLL = windows.NewLazySystemDLL("dwrite.dll")
	ole32DLL  = windows.NewLazySystemDLL("ole32.dll")

	pD2D1CreateFactory   = d2d1DLL.NewProc("D2D1CreateFactory")
	pDWriteCreateFactory = dwriteDLL.NewProc("DWriteCreateFactory")
	pCoInitializeEx      = ole32DLL.NewProc("CoInitializeEx")
)

// IID：ID2D1Factory / ID2D1DCRenderTarget / IDWriteFactory。
var (
	iidD2DFactory = guid{0x06152247, 0x6f50, 0x465a, [8]byte{0x92, 0x45, 0x11, 0x8b, 0xfd, 0x3b, 0x60, 0x07}}
	iidD2DDCRT    = guid{0x1c51ae64, 0x61d9, 0x4fd9, [8]byte{0x8f, 0x19, 0x64, 0x1f, 0x27, 0x2c, 0x95, 0x25}}
	iidDWFactory  = guid{0xb859ee5a, 0xd838, 0x4b5b, [8]byte{0xa2, 0xe8, 0x1a, 0xdc, 0x7d, 0x93, 0xdb, 0x48}}
)

type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

// COM 初始化幂等（UI 线程通常已被 walk/OLE 初始化，重复调用无害）。
var coInitOnce sync.Once

func coInit() {
	coInitOnce.Do(func() {
		const coinitApartmentthreaded = 0x2
		_, _, _ = pCoInitializeEx.Call(0, coinitApartmentthreaded)
	})
}

// vtCall 调用 COM 对象第 index 个虚表方法（0=QI,1=AddRef,2=Release）。
// COM 对象首字段是 vtable 指针，需两次解引用取到函数指针
// （数组式索引是 vet 认可的写法）。
func vtCall(obj unsafe.Pointer, index int, args ...uintptr) (uintptr, uintptr, error) {
	vt := *(*unsafe.Pointer)(obj)
	fp := (*[1 << 20]unsafe.Pointer)(vt)[index]
	callArgs := append([]uintptr{uintptr(obj)}, args...)
	r1, r2, err := syscall.SyscallN(uintptr(fp), callArgs...)
	return r1, r2, err
}

func release(obj unsafe.Pointer) {
	if obj != nil {
		_, _, _ = vtCall(obj, 2)
	}
}

func hrErr(hr uintptr, what string) error {
	return fmt.Errorf("win: %s 失败 hr=0x%08x", what, uint32(hr))
}

// D2D1 渲染目标属性：默认类型、BGRA 预乘、96dpi（1 DIP=1px）、GDI 兼容。
type rtProps struct {
	typ                   uint32
	dxgiFormat, alphaMode uint32
	dpiX, dpiY            float32
	usage, minLevel       uint32
}

// TextRenderer 把彩色文本（含 emoji）画到 HDC 上。非并发安全，
// 仅供 UI 线程使用；进程生命周期内复用一份。
type TextRenderer struct {
	factory   unsafe.Pointer // ID2D1Factory
	dwFactory unsafe.Pointer // IDWriteFactory
	target    unsafe.Pointer // ID2D1DCRenderTarget
	ellipsis  unsafe.Pointer // IDWriteInlineObject（省略号截断符）
	fmtBig    unsafe.Pointer // IDWriteTextFormat：内容行
	fmtSmall  unsafe.Pointer // IDWriteTextFormat：时间行
	brushes   map[uint32]unsafe.Pointer
}

var (
	textRendererOnce sync.Once
	textRenderer     *TextRenderer
	textRendererErr  error
)

// GetTextRenderer 惰性初始化全局渲染器（dpi 为使用窗口的 DPI，
// 决定字号物理像素）。初始化失败返回错误，调用方回退 GDI 且不再重试。
func GetTextRenderer(dpi int) (*TextRenderer, error) {
	textRendererOnce.Do(func() {
		textRenderer, textRendererErr = newTextRenderer(dpi)
	})
	return textRenderer, textRendererErr
}

func newTextRenderer(dpi int) (*TextRenderer, error) {
	coInit()
	r := &TextRenderer{brushes: map[uint32]unsafe.Pointer{}}

	var factory unsafe.Pointer
	hr, _, _ := pD2D1CreateFactory.Call(0, uintptr(unsafe.Pointer(&iidD2DFactory)), 0,
		uintptr(unsafe.Pointer(&factory)))
	if hr != 0 || factory == nil {
		return nil, hrErr(hr, "D2D1CreateFactory")
	}
	r.factory = factory

	props := rtProps{
		typ:        0,                // D2D1_RENDER_TARGET_TYPE_DEFAULT
		dxgiFormat: 87, alphaMode: 1, // B8G8R8A8_UNORM / PREMULTIPLIED
		dpiX: 96, dpiY: 96,
		usage: 2, // GDI_COMPATIBLE
	}
	var target unsafe.Pointer
	hr, _, _ = vtCall(factory, 16, // ID2D1Factory::CreateDCRenderTarget
		uintptr(unsafe.Pointer(&props)), uintptr(unsafe.Pointer(&iidD2DDCRT)),
		uintptr(unsafe.Pointer(&target)))
	if hr != 0 || target == nil {
		release(factory)
		return nil, hrErr(hr, "CreateDCRenderTarget")
	}
	r.target = target

	var dwf unsafe.Pointer
	hr, _, _ = pDWriteCreateFactory.Call(0, uintptr(unsafe.Pointer(&iidDWFactory)),
		uintptr(unsafe.Pointer(&dwf)))
	if hr != 0 || dwf == nil {
		return nil, hrErr(hr, "DWriteCreateFactory")
	}
	r.dwFactory = dwf

	family, _ := syscall.UTF16PtrFromString("Segoe UI")
	locale, _ := syscall.UTF16PtrFromString("")
	// DIP=px（目标 96dpi）：字号按窗口 DPI 折算物理像素
	mk := func(point float32) (unsafe.Pointer, error) {
		size := point * float32(dpi) / 72
		var f unsafe.Pointer
		hr, _, _ := vtCall(dwf, 15, // CreateTextFormat
			uintptr(unsafe.Pointer(family)), 0,
			400 /*NORMAL*/, 0, 5, /*STRETCH_NORMAL*/
			uintptr(mathFloat32bits(size)), uintptr(unsafe.Pointer(locale)),
			uintptr(unsafe.Pointer(&f)))
		if hr != 0 || f == nil {
			return nil, hrErr(hr, "CreateTextFormat")
		}
		return f, nil
	}
	var err error
	if r.fmtBig, err = mk(10); err != nil {
		return nil, err
	}
	if r.fmtSmall, err = mk(8); err != nil {
		return nil, err
	}

	var ell unsafe.Pointer
	hr, _, _ = vtCall(dwf, 20, 0, uintptr(unsafe.Pointer(&ell))) // CreateEllipsisTrimmingSign(NULL)
	if hr != 0 {
		ell = nil // 省略号不可用不致命：仅失去尾部省略
	}
	r.ellipsis = ell
	return r, nil
}

func mathFloat32bits(f float32) uint32 { return *(*uint32)(unsafe.Pointer(&f)) }

// brushFor 取/建颜色画刷（COLORREF 0x00BBGGRR → D2D RGBA）。
func (r *TextRenderer) brushFor(color uint32) (unsafe.Pointer, error) {
	if b, ok := r.brushes[color]; ok {
		return b, nil
	}
	d2dColor := [4]float32{
		float32((color>>16)&0xff) / 255, // R
		float32((color>>8)&0xff) / 255,  // G
		float32(color&0xff) / 255,       // B
		1,
	}
	var b unsafe.Pointer
	hr, _, _ := vtCall(r.target, 8, // CreateSolidColorBrush
		uintptr(unsafe.Pointer(&d2dColor)), 0, uintptr(unsafe.Pointer(&b)))
	if hr != 0 || b == nil {
		return nil, hrErr(hr, "CreateSolidColorBrush")
	}
	r.brushes[color] = b
	return b, nil
}

// DrawText 在 hdc 的 (x,y,w,h) 矩形内画一行文本（垂直居中、左对齐、
// 超宽省略号截断、彩色 emoji）。hdc 为 walk Canvas 的 HDC。
func (r *TextRenderer) DrawText(hdc uintptr, text string, x, y, w, h int32, color uint32, big bool) error {
	if r.target == nil || hdc == 0 || text == "" {
		return fmt.Errorf("win: DrawText 参数无效")
	}
	w16, err := syscall.UTF16FromString(text)
	if err != nil {
		return err
	}
	fmtObj := r.fmtSmall
	if big {
		fmtObj = r.fmtBig
	}
	var layout unsafe.Pointer
	hr, _, _ := vtCall(r.dwFactory, 18, // CreateTextLayout
		uintptr(unsafe.Pointer(&w16[0])), uintptr(len(w16)-1), uintptr(fmtObj),
		uintptr(mathFloat32bits(float32(w))), uintptr(mathFloat32bits(float32(h))),
		uintptr(unsafe.Pointer(&layout)))
	if hr != 0 || layout == nil {
		return hrErr(hr, "CreateTextLayout")
	}
	defer release(layout)

	// 不换行 + 段落垂直居中 + 字符级省略截断
	_, _, _ = vtCall(layout, 5, 1) // SetWordWrapping(NO_WRAP)
	_, _, _ = vtCall(layout, 4, 2) // SetParagraphAlignment(CENTER)
	if r.ellipsis != nil {
		trim := [3]uint32{1 /*CHARACTER*/, 0, 0}
		_, _, _ = vtCall(layout, 9, uintptr(unsafe.Pointer(&trim)), uintptr(r.ellipsis)) // SetTrimming
	}

	rect := [4]int32{x, y, x + w, y + h}
	hr, _, _ = vtCall(r.target, 57, hdc, uintptr(unsafe.Pointer(&rect))) // BindDC
	if hr != 0 {
		return hrErr(hr, "BindDC")
	}
	brush, err := r.brushFor(color)
	if err != nil {
		return err
	}

	_, _, _ = vtCall(r.target, 48) // BeginDraw
	transparent := [4]float32{0, 0, 0, 0}
	_, _, _ = vtCall(r.target, 47, uintptr(unsafe.Pointer(&transparent))) // Clear
	origin := [2]float32{float32(x), float32(y)}
	hr, _, _ = vtCall(r.target, 28, // DrawTextLayout
		uintptr(unsafe.Pointer(&origin)), uintptr(layout), uintptr(brush), 0)
	if hr != 0 {
		return hrErr(hr, "DrawTextLayout")
	}
	hr2, _, _ := vtCall(r.target, 49, 0, 0)    // EndDraw
	if hr2 != 0 && uint32(hr2) == 0x88990012 { // D2DERR_RECREATE_TARGET
		// 目标失效：作废画刷缓存（画刷绑定在旧目标上）
		for _, b := range r.brushes {
			release(b)
		}
		r.brushes = map[uint32]unsafe.Pointer{}
	}
	return nil
}
