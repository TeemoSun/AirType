//go:build windows

package ui

import (
	"image"
	"image/color"
	"math"
	"sync"

	"github.com/lxn/walk"
)

// 抗锯齿圆角矩形的纯 Go 实现。为什么不用现成原语：
//   - walk 自带 FillRoundedRectangle 走 GDI RoundRect，无抗锯齿，锯齿明显；
//   - GDI+ Flat API（GdipAddPathArc 等）参数是裸 float，x64 调用约定下
//     float 走 XMM 寄存器，Go 的 syscall 只能传整型寄存器，够不着
//     （上一次现代化在 D2D 上也踩过同一 ABI 边界，只能传按值结构体）。
// 改为解析计算：对每个角部像素求"像素中心到圆角圆形边界的有向距离"，
// 覆盖率 = clamp(0.5-d, 0, 1)，生成带 alpha 通道的位图（Go 侧 RGBA()
// 自动预乘，walk 的 AlphaBlend 逐像素合成），边缘即平滑过渡到窗口底色。
// 位图按 (宽,高,半径,颜色,DPI) 缓存，绘制就是一次贴图。

var (
	roundMu      sync.Mutex
	roundBitmaps = map[roundKey]*walk.Bitmap{}
)

type roundKey struct {
	w, h, r int
	c       walk.Color
	dpi     int
}

// fillRoundRect 画抗锯齿圆角填充矩形（rect 为像素坐标，radius 为 96dpi
// 逻辑单位）。位图链路失败时回退 GDI 方角填充（观感退化，功能无损）。
func fillRoundRect(canvas *walk.Canvas, rect walk.Rectangle, radius96 int, c walk.Color) error {
	bmp, ok := roundedRectBitmap(rect.Width, rect.Height,
		walk.IntFrom96DPI(radius96, canvas.DPI()), c, canvas.DPI())
	if !ok {
		return fillSquareRect(canvas, rect, c)
	}
	return canvas.DrawImagePixels(bmp, walk.Point{X: rect.X, Y: rect.Y})
}

// strokeRoundRect 画 1 物理像素圆角描边：外圈整幅 stroke 色，内芯缩 1px
// 填 fill 色。绘制面都是纯色窗口底，双层叠加等效描边（Win11 控件同法）。
func strokeRoundRect(canvas *walk.Canvas, rect walk.Rectangle, radius96 int, stroke, fill walk.Color) error {
	if bmp, ok := roundedRectBitmap(rect.Width, rect.Height,
		walk.IntFrom96DPI(radius96, canvas.DPI()), stroke, canvas.DPI()); ok {
		if err := canvas.DrawImagePixels(bmp, walk.Point{X: rect.X, Y: rect.Y}); err != nil {
			return err
		}
	} else {
		if err := fillSquareRect(canvas, rect, stroke); err != nil {
			return err
		}
	}
	inner := walk.Rectangle{X: rect.X + 1, Y: rect.Y + 1, Width: rect.Width - 2, Height: rect.Height - 2}
	return fillRoundRect(canvas, inner, radius96, fill)
}

// fillSquareRect 方角兜底：刷子即建即弃。
func fillSquareRect(canvas *walk.Canvas, rect walk.Rectangle, c walk.Color) error {
	br, err := walk.NewSolidColorBrush(c)
	if err != nil {
		return err
	}
	defer br.Dispose()
	return canvas.FillRectanglePixels(br, rect)
}

// roundedRectBitmap 取（或生成并缓存）指定参数的圆角位图。
// ok=false 表示半径被钳成 0 或生成失败，调用方走方角路径。
func roundedRectBitmap(w, h, r int, c walk.Color, dpi int) (bmp *walk.Bitmap, ok bool) {
	if w <= 0 || h <= 0 {
		return nil, false
	}
	if max := (min(w, h) - 2) / 2; r > max { // 留 1px 直边，避免整幅都是圆
		r = max
	}
	if r < 2 {
		return nil, false
	}
	key := roundKey{w: w, h: h, r: r, c: c, dpi: dpi}
	roundMu.Lock()
	defer roundMu.Unlock()
	bmp = roundBitmaps[key]
	if bmp != nil {
		return bmp, true
	}
	var err error
	if bmp, err = walk.NewBitmapFromImageForDPI(roundRectImage(w, h, r, c), dpi); err != nil {
		return nil, false
	}
	roundBitmaps[key] = bmp
	// 防御性上限：尺寸频繁变化的场景（如滚动条随条目数伸缩）也不该积累
	// 过多；真超限整体清空重来，单张位图至多几十万像素，代价可忽略。
	if len(roundBitmaps) > 128 {
		for k, b := range roundBitmaps {
			if k != key {
				b.Dispose()
				delete(roundBitmaps, k)
			}
		}
	}
	return bmp, true
}

// roundRectImage 生成圆角矩形的抗锯齿遮罩图：底色全不透明，四个角部
// 方形区域内逐像素按圆的覆盖距离衰减 alpha（r ≤ min(w,h)/2-1 由调用方
// 钳好，四角区域互不重叠）。
func roundRectImage(w, h, r int, c walk.Color) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	cc := color.NRGBA{R: c.R(), G: c.G(), B: c.B(), A: 0xFF}
	for y := 0; y < h; y++ {
		row := img.Pix[y*img.Stride:]
		for x := 0; x < w; x++ {
			i := x * 4
			row[i], row[i+1], row[i+2], row[i+3] = cc.R, cc.G, cc.B, cc.A
		}
	}
	cornerAlpha := func(x, y, cx, cy int) uint8 {
		// 有向距离 d：负 = 在圆内。像素中心取 (x+0.5, y+0.5)。
		dx := float64(x) + 0.5 - float64(cx)
		dy := float64(y) + 0.5 - float64(cy)
		d := math.Sqrt(dx*dx+dy*dy) - float64(r)
		a := 0.5 - d
		if a <= 0 {
			return 0
		}
		if a >= 1 {
			return 0xFF
		}
		return uint8(a*255 + 0.5)
	}
	for y := 0; y < r && y < h; y++ {
		for x := 0; x < r && x < w; x++ {
			// 四角按对称镜像各算一次：像素 (x,y) 与镜像点的有向距离一致
			img.Pix[y*img.Stride+x*4+3] = cornerAlpha(x, y, r, r)                             // 左上
			img.Pix[y*img.Stride+(w-1-x)*4+3] = cornerAlpha(w-1-x, y, w-1-r, r)               // 右上
			img.Pix[(h-1-y)*img.Stride+x*4+3] = cornerAlpha(x, h-1-y, r, h-1-r)               // 左下
			img.Pix[(h-1-y)*img.Stride+(w-1-x)*4+3] = cornerAlpha(w-1-x, h-1-y, w-1-r, h-1-r) // 右下
		}
	}
	return img
}
