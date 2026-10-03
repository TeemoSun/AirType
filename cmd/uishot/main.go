//go:build windows

// uishot 开发工具：拉起 AirType 的真实 UI 并截图为 PNG，供视觉评审。
// 不连网、不注入键盘；输出目录由第一个参数指定（默认 %TEMP%\airtype-uishot），
// 全部截图完成后进程自动退出。
//
// 产物：
//
//	01-chooser.png   通道选择窗口（未绑定态的登录入口）
//	02-qr.png        微信扫码窗口（QQ 绑定共用同一布局）
//	03-popup-empty.png 历史弹窗·空态
//	04-popup.png     历史弹窗·有历史（斑马纹/截断/相对时间）
//	05-menu.png      托盘右键菜单（同款原生菜单模拟：勾选项+分隔线）
//	06-icons.png     三态图标在浅/深任务栏底色下的 16/24/32px 渲染
package main

import (
	"fmt"
	"image"
	"image/png"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"github.com/TeemoSun/AirType/internal/history"
	"github.com/TeemoSun/AirType/internal/ui"
	"github.com/TeemoSun/AirType/internal/win"
)

var (
	user32 = syscall.NewLazyDLL("user32.dll")
	gdi32  = syscall.NewLazyDLL("gdi32.dll")

	pGetWindowRect    = user32.NewProc("GetWindowRect")
	pGetDC            = user32.NewProc("GetDC")
	pReleaseDC        = user32.NewProc("ReleaseDC")
	pSetCursorPos     = user32.NewProc("SetCursorPos")
	pFindWindowW      = user32.NewProc("FindWindowW")
	pGetSystemMetrics = user32.NewProc("GetSystemMetrics")
	pCreatePopupMenu  = user32.NewProc("CreatePopupMenu")
	pAppendMenuW      = user32.NewProc("AppendMenuW")
	pTrackPopupMenuEx = user32.NewProc("TrackPopupMenuEx")
	pDestroyMenu      = user32.NewProc("DestroyMenu")
	pCreateWindowExW  = user32.NewProc("CreateWindowExW")
	pDestroyWindow    = user32.NewProc("DestroyWindow")

	pCreateCompatibleDC     = gdi32.NewProc("CreateCompatibleDC")
	pCreateCompatibleBitmap = gdi32.NewProc("CreateCompatibleBitmap")
	pSelectObject           = gdi32.NewProc("SelectObject")
	pBitBlt                 = gdi32.NewProc("BitBlt")
	pDeleteDC               = gdi32.NewProc("DeleteDC")
	pDeleteObject           = gdi32.NewProc("DeleteObject")
	pGetDIBits              = gdi32.NewProc("GetDIBits")
)

const outDirEnv = "UISHOT_OUT"

var (
	logger *slog.Logger
	outDir string
)

func main() {
	logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	outDir = os.Getenv(outDirEnv)
	if outDir == "" {
		outDir = filepath.Join(os.TempDir(), "airtype-uishot")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		logger.Error("创建输出目录失败", "err", err)
		os.Exit(1)
	}

	hist, err := history.Open(filepath.Join(outDir, "uishot-history.json"), 500)
	if err != nil {
		logger.Error("打开历史失败", "err", err)
		os.Exit(1)
	}

	tray, err := ui.NewTray(ui.Config{
		Logger:     logger,
		History:    hist,
		InjectText: func(string) error { return nil },
		TogglePause: func() bool {
			return false
		},
		AutoEnterEnabled: func() bool { return false },
		ToggleAutoEnter:  func() bool { return false },
		ChooseChannel: func(c ui.ChannelChoice) {
			logger.Info("ChooseChannel", "choice", c)
		},
		Logout:           func() {},
		OpenLog:          func() {},
		AutostartEnabled: func() bool { return false },
		AutostartSet:     func(bool) error { return nil },
	})
	if err != nil {
		logger.Error("创建托盘失败", "err", err)
		os.Exit(1)
	}
	hist.OnChange(tray.HistoryChanged)

	go scenario(tray, hist)
	tray.Run()
}

func scenario(tray *ui.Tray, hist *history.Store) {
	cx, cy := screenSize()
	pSetCursorPos.Call(uintptr(cx/2), uintptr(cy/2))

	// 1) 通道选择窗口
	callWatchdog(func() { tray.ShowChannelChooser() }, "ShowChannelChooser")
	sleep(900)
	shootByTitle("AirType · 选择绑定通道", "01-chooser.png")

	// 2) 微信扫码窗口
	tray.ShowQR("https://login.weixin.qq.com/l/AirTypeDemo123")
	sleep(1000)
	shootByTitle("AirType · 扫码绑定微信", "02-qr.png")
	tray.HideQR()

	// 3) 历史弹窗 · 空态
	tray.SetState(ui.StateConnected)
	callWatchdog(tray.ShowHistory, "ShowHistory(empty)")
	sleep(800)
	shootByTitle("AirType 历史", "03-popup-empty.png")
	tray.HidePopupForTest()

	// 4) 历史弹窗 · 满态
	for _, s := range []string{
		"帮我订明天下午三点的会议室，要带投屏的那种大房间，最好能容纳十个人以上，记得看看有没有白板",
		"npm run build -- --mode production && npm run deploy",
		"收到，我马上过去 😀",
		"https://github.com/TeemoSun/AirType/releases/tag/v1.1.0",
		"微信里复制的验证码 8848",
		"TODO: fix the race condition in gateway reconnect loop before shipping",
		"你好",
		"第 3 行会显示相对时间（刚刚），过一分钟会变成 1 分钟前",
		"🚀🚀🚀",
		"The quick brown fox jumps over the lazy dog 0123456789",
	} {
		hist.Add(s)
	}
	sleep(200)
	callWatchdog(tray.ShowHistory, "ShowHistory(filled)")
	sleep(900)
	shootByTitle("AirType 历史", "04-popup.png")
	tray.HidePopupForTest()

	// 5) 托盘菜单模拟（与真实菜单同款原生菜单：两个勾选项 + 分隔线）
	menuOK := shootMenu("05-menu.png")
	if !menuOK {
		logger.Error("菜单截图失败")
	}

	// 6) 三态图标渲染
	makeIconStrip("06-icons.png")

	logger.Info("全部截图完成", "dir", outDir)
	sleep(400)
	os.Exit(0)
}

// ---- 截图与 Win32 辅助 ----

type wRect struct{ Left, Top, Right, Bottom int32 }

func screenSize() (int32, int32) {
	cx, _, _ := pGetSystemMetrics.Call(0) // SM_CXSCREEN
	cy, _, _ := pGetSystemMetrics.Call(1) // SM_CYSCREEN
	return int32(cx), int32(cy)
}

func findWindow(class, title string) uintptr {
	var cp, tp uintptr
	if class != "" {
		p, _ := syscall.UTF16PtrFromString(class)
		cp = uintptr(unsafe.Pointer(p))
	}
	if title != "" {
		p, _ := syscall.UTF16PtrFromString(title)
		tp = uintptr(unsafe.Pointer(p))
	}
	h, _, _ := pFindWindowW.Call(cp, tp)
	return h
}

func shootByTitle(title, name string) {
	h := findWindow("", title)
	if h == 0 {
		logger.Error("找不到窗口", "title", title)
		return
	}
	var rc wRect
	pGetWindowRect.Call(h, uintptr(unsafe.Pointer(&rc)))
	if err := shootRect(filepath.Join(outDir, name), rc.Left, rc.Top, rc.Right-rc.Left, rc.Bottom-rc.Top); err != nil {
		logger.Error("截图失败", "name", name, "err", err)
	} else {
		logger.Info("已截图", "name", name, "size", fmt.Sprintf("%dx%d", rc.Right-rc.Left, rc.Bottom-rc.Top))
	}
}

// shootRect 把屏幕区域 (x,y,w,h) 用 BitBlt 截成 PNG（窗口须实际可见）。
func shootRect(path string, x, y, w, h int32) error {
	if w <= 0 || h <= 0 {
		return fmt.Errorf("非法尺寸 %dx%d", w, h)
	}
	hdc, _, _ := pGetDC.Call(0)
	defer pReleaseDC.Call(0, hdc)
	mem, _, _ := pCreateCompatibleDC.Call(hdc)
	defer pDeleteDC.Call(mem)
	hbm, _, _ := pCreateCompatibleBitmap.Call(hdc, uintptr(w), uintptr(h))
	defer pDeleteObject.Call(hbm)
	old, _, _ := pSelectObject.Call(mem, hbm)
	defer pSelectObject.Call(mem, old)
	const srcCopy = 0x00CC0020
	pBitBlt.Call(mem, 0, 0, uintptr(w), uintptr(h), hdc, uintptr(x), uintptr(y), srcCopy)

	type bmih struct {
		Size, Width, Height    int32
		Planes, BitCount       uint16
		Compression, SizeImage int32
		XPPM, YPPM             int32
		ClrUsed, ClrImportant  uint32
	}
	var hdr bmih
	hdr.Size, hdr.Width, hdr.Height = 40, w, -h // 负高 = top-down
	hdr.Planes, hdr.BitCount = 1, 32
	buf := make([]byte, int(w)*int(h)*4)
	ret, _, _ := pGetDIBits.Call(hdc, hbm, 0, uintptr(uint32(h)),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&hdr)), 0)
	if ret == 0 {
		return fmt.Errorf("GetDIBits 失败")
	}
	img := image.NewNRGBA(image.Rect(0, 0, int(w), int(h)))
	for i := 0; i < int(w)*int(h); i++ {
		b := buf[i*4 : i*4+4] // BGRA → RGBA
		img.Pix[i*4+0] = b[2]
		img.Pix[i*4+1] = b[1]
		img.Pix[i*4+2] = b[0]
		img.Pix[i*4+3] = 255
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

// shootMenu 在主屏中部弹出与托盘菜单同构的原生菜单（含勾选/分隔线），
// 截取菜单窗口（类名 #32768）。
// 关键点：TrackPopupMenuEx 的 owner 必须与调用方同线程，因此这里
// LockOSThread 后自建一个宿主窗口再弹菜单。
func shootMenu(name string) bool {
	cx, cy := screenSize()
	pSetCursorPos.Call(uintptr(cx/3), uintptr(cy/3))

	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		const wsPopup, wsVisible = 0x80000000, 0x10000000
		cls, _ := syscall.UTF16PtrFromString("STATIC")
		title, _ := syscall.UTF16PtrFromString("AirType MenuHost")
		host, _, _ := pCreateWindowExW.Call(0,
			uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(title)),
			wsPopup|wsVisible,
			uintptr(cx/3-80), uintptr(cy/3-18), 150, 36, 0, 0, 0, 0)
		if host == 0 {
			logger.Error("创建菜单宿主窗口失败")
			return
		}
		defer pDestroyWindow.Call(host)
		if err := win.Activate(win.Hwnd(host), 2*time.Second); err != nil {
			logger.Warn("激活宿主失败（菜单仍尝试弹出）", "err", err)
		}

		hmenu, _, _ := pCreatePopupMenu.Call()
		if hmenu == 0 {
			return
		}
		defer pDestroyMenu.Call(hmenu)
		const mfString, mfChecked, mfSeparator = 0x0, 0x8, 0x800
		add := func(flags uintptr, id uintptr, text string) {
			p, _ := syscall.UTF16PtrFromString(text)
			pAppendMenuW.Call(hmenu, flags, id, uintptr(unsafe.Pointer(p)))
		}
		add(mfString, 1, "查看历史")
		add(mfString, 2, "暂停打字")
		add(mfString|mfChecked, 3, "自动回车")
		pAppendMenuW.Call(hmenu, mfSeparator, 0, 0)
		add(mfString, 4, "切换通道…")
		pAppendMenuW.Call(hmenu, mfSeparator, 0, 0)
		add(mfString, 5, "打开日志")
		add(mfString, 6, "清空历史")
		pAppendMenuW.Call(hmenu, mfSeparator, 0, 0)
		add(mfString|mfChecked, 7, "开机自启")
		pAppendMenuW.Call(hmenu, mfSeparator, 0, 0)
		add(mfString, 8, "退出")
		const tpmReturnCmd, tpmRightButton = 0x0100, 0x0002
		pTrackPopupMenuEx.Call(hmenu, tpmReturnCmd|tpmRightButton,
			uintptr(cx/3), uintptr(cy/3), host, 0)
	}()

	sleep(900)
	mh := findWindow("#32768", "")
	if mh == 0 {
		logger.Error("未找到菜单窗口")
		return false
	}
	var rc wRect
	pGetWindowRect.Call(mh, uintptr(unsafe.Pointer(&rc)))
	if err := shootRect(filepath.Join(outDir, name), rc.Left, rc.Top, rc.Right-rc.Left, rc.Bottom-rc.Top); err != nil {
		logger.Error("菜单截图失败", "err", err)
		return false
	}
	logger.Info("已截图", "name", name)
	return true
}

// makeIconStrip 纯 Go 合成三态图标的渲染条（不依赖 walk，跨线程安全）：
//
//	行1 浅任务栏底 #F3F3F3：green/yellow/red 各 16/24/32px
//	行2 深任务栏底 #202020：同上
//	行3 白底：三态 64px（看造型细节）
//
// 缩放用 box-filter（块平均），近似系统线性缩放的观感。
func makeIconStrip(name string) {
	states := []string{"green", "yellow", "red"}
	smallSizes := []int{16, 24, 32}
	const rowH, row3H, pad, gap = 56, 84, 20, 14
	w := pad*2 + 3*(32+gap) + 3*(64+gap)

	img := image.NewNRGBA(image.Rect(0, 0, w, rowH*2+row3H))
	fillRect(img, image.Rect(0, 0, w, rowH), 243, 243, 243)
	fillRect(img, image.Rect(0, rowH, w, rowH*2), 32, 32, 32)
	fillRect(img, image.Rect(0, rowH*2, w, rowH*2+row3H), 255, 255, 255)

	drawIcon := func(state string, size, x, rowY int) {
		src, err := loadIconImage(state)
		if err != nil {
			logger.Error("读图标失败", "state", state, "err", err)
			return
		}
		s := boxScale(src, size)
		for yy := 0; yy < size; yy++ {
			for xx := 0; xx < size; xx++ {
				c := s.At(xx, yy)
				r, g, b, a := c.RGBA()
				if a == 0 {
					continue
				}
				blendPixel(img, image.Pt(x+xx, rowY+yy), uint8(r>>8), uint8(g>>8), uint8(b>>8), uint8(a>>8))
			}
		}
	}

	x := pad
	for _, st := range states {
		for _, sz := range smallSizes {
			drawIcon(st, sz, x, (rowH-sz)/2)
			x += sz + gap/2
		}
		x += gap
	}
	x = pad
	for _, st := range states {
		for _, sz := range smallSizes {
			drawIcon(st, sz, x, rowH+(rowH-sz)/2)
			x += sz + gap/2
		}
		x += gap
	}
	x = pad
	for _, st := range states {
		drawIcon(st, 64, x, rowH*2+(row3H-64)/2)
		x += 64 + gap
	}

	f, err := os.Create(filepath.Join(outDir, name))
	if err != nil {
		logger.Error("写图标条失败", "err", err)
		return
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		logger.Error("编码图标条失败", "err", err)
		return
	}
	logger.Info("已生成", "name", name)
}

func fillRect(dst *image.NRGBA, r image.Rectangle, red, green, blue uint8) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			i := dst.PixOffset(x, y)
			dst.Pix[i], dst.Pix[i+1], dst.Pix[i+2], dst.Pix[i+3] = red, green, blue, 255
		}
	}
}

// blendPixel 在 dst 上做 source-over 混合（颜色为 straight alpha）。
func blendPixel(dst *image.NRGBA, p image.Point, r, g, b, a uint8) {
	i := dst.PixOffset(p.X, p.Y)
	ai := float64(a) / 255
	dr, dg, db := float64(dst.Pix[i]), float64(dst.Pix[i+1]), float64(dst.Pix[i+2])
	dst.Pix[i] = uint8(float64(r)*ai + dr*(1-ai))
	dst.Pix[i+1] = uint8(float64(g)*ai + dg*(1-ai))
	dst.Pix[i+2] = uint8(float64(b)*ai + db*(1-ai))
	dst.Pix[i+3] = 255
}

// boxScale 把任意图像 box-filter 缩放到 size×size（块平均，颜色按 alpha 加权）。
func boxScale(src image.Image, size int) *image.NRGBA {
	b := src.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			sx0 := b.Min.X + x*b.Dx()/size
			sx1 := b.Min.X + (x+1)*b.Dx()/size
			sy0 := b.Min.Y + y*b.Dy()/size
			sy1 := b.Min.Y + (y+1)*b.Dy()/size
			if sx1 <= sx0 {
				sx1 = sx0 + 1
			}
			if sy1 <= sy0 {
				sy1 = sy0 + 1
			}
			var sr, sg, sb, sa, n float64
			for sy := sy0; sy < sy1; sy++ {
				for sx := sx0; sx < sx1; sx++ {
					r, g, b_, a := src.At(sx, sy).RGBA()
					af := float64(a) / 65535
					sr += float64(r>>8) * af
					sg += float64(g>>8) * af
					sb += float64(b_>>8) * af
					sa += af
					n++
				}
			}
			if n == 0 || sa == 0 {
				continue
			}
			i := dst.PixOffset(x, y)
			dst.Pix[i] = uint8(sr / sa)
			dst.Pix[i+1] = uint8(sg / sa)
			dst.Pix[i+2] = uint8(sb / sa)
			dst.Pix[i+3] = uint8(sa / n * 255)
		}
	}
	return dst
}

func loadIconImage(state string) (image.Image, error) {
	f, err := os.Open(filepath.Join("internal", "ui", "icons", state+".png"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return png.Decode(f)
}

func sleep(ms int) { time.Sleep(time.Duration(ms) * time.Millisecond) }

// callWatchdog 执行 UI 调用，3 秒未返回按死锁放弃（沿用 traytest 手法）。
func callWatchdog(fn func(), name string) {
	done := make(chan struct{})
	go func() {
		fn()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		logger.Error("UI 调用超时", "name", name)
	}
}
