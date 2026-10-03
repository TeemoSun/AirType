//go:build windows

// icongen 开发工具：程序化生成 AirType 四态托盘图标（256px 主图 + 16px 极简版）。
//
// 设计：31 号"斜置键盘"语言——圆角方形渐变底 + 白色键帽（两排键 + 空格条），
// 整体旋转约 -12°。256px 保留完整键盘细节；16px 走极简变体（一枚大键帽 +
// 空格条），避免按键阵在托盘真实尺寸下糊成一片。
//
// 生成物写入 internal/ui/icons/：
//
//	{green,yellow,red,gray}.png     256px 主图（含 20% 内边距与柔和投影）
//	{green,yellow,red,gray}-16.png  16px 极简版（按物理像素 1:1 光栅化）
//	{green,yellow,red,gray}-24.png  24px 中间版
//
// 运行：go run ./cmd/icongen
package main

import (
	"flag"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

// stateDef 是一个状态的配色（渐变起止 + 点缀色）。
type stateDef struct {
	name     string
	from, to color.NRGBA // 渐变（左上→右下）
}

// 配色与状态语义：绿=正常、黄=需要注意、红=出错、灰=未绑定。
// 渐变收窄为同色系明度差（评审结论：大跨度渐变在小尺寸下互相难分）。
var states = []stateDef{
	{"green", nrgb(46, 184, 108), nrgb(28, 148, 84)},
	{"yellow", nrgb(244, 177, 42), nrgb(214, 140, 16)},
	{"red", nrgb(240, 96, 96), nrgb(208, 56, 56)},
	{"gray", nrgb(148, 156, 164), nrgb(112, 120, 128)},
}

func nrgb(r, g, b uint8) color.NRGBA { return color.NRGBA{R: r, G: g, B: b, A: 255} }

func main() {
	out := flag.String("out", filepath.Join("internal", "ui", "icons"), "输出目录")
	flag.Parse()
	if err := os.MkdirAll(*out, 0o755); err != nil {
		panic(err)
	}
	for _, st := range states {
		if err := savePNG(filepath.Join(*out, st.name+".png"), renderMaster(st)); err != nil {
			panic(err)
		}
		if err := savePNG(filepath.Join(*out, st.name+"-16.png"), renderSmall(st, 16)); err != nil {
			panic(err)
		}
		if err := savePNG(filepath.Join(*out, st.name+"-24.png"), renderSmall(st, 24)); err != nil {
			panic(err)
		}
		if err := savePNG(filepath.Join(*out, st.name+"-32.png"), renderSmall(st, 32)); err != nil {
			panic(err)
		}
	}
}

func savePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

// ---------- 数学与绘制基元（超采样抗锯齿：每像素 3×3 采样） ----------

const ss = 3 // 超采样倍数

// coverage 对形状函数做 3×3 超采样，返回 [0,1] 覆盖率。
func coverage(px, py float64, inside func(x, y float64) bool) float64 {
	hit := 0
	for i := 0; i < ss; i++ {
		for j := 0; j < ss; j++ {
			x := px + (float64(i)+0.5)/ss
			y := py + (float64(j)+0.5)/ss
			if inside(x, y) {
				hit++
			}
		}
	}
	return float64(hit) / (ss * ss)
}

// roundRectInside 生成圆角矩形（cx,cy 中心，w,h 尺寸，r 圆角）的 inside 函数。
func roundRect(cx, cy, w, h, r float64) func(float64, float64) bool {
	x0, y0 := cx-w/2, cy-h/2
	x1, y1 := cx+w/2, cy+h/2
	return func(x, y float64) bool {
		if x < x0 || x > x1 || y < y0 || y > y1 {
			return false
		}
		// 四角圆角判定
		dx := math.Max(x0+r-x, x-(x1-r))
		dy := math.Max(y0+r-y, y-(y1-r))
		return dx <= 0 || dy <= 0 || dx*dx+dy*dy <= r*r
	}
}

// rotate 围绕 (cx,cy) 把点旋转 -ang 弧度（形状 ang 倾斜等价于反向转采样点）。
func rot(x, y, cx, cy, ang float64) (float64, float64) {
	s, c := math.Sincos(ang)
	dx, dy := x-cx, y-cy
	return cx + dx*c - dy*s, cy + dx*s + dy*c
}

// ---------- 256px 主图 ----------

func renderMaster(st stateDef) image.Image {
	const S = 256.0
	img := image.NewNRGBA(image.Rect(0, 0, 256, 256))
	white := color.NRGBA{255, 255, 255, 255}

	// 底板：圆角方形（占画面 68%），旋转 -12°，柔和投影近似（底边深色椭圆环）
	const ang = -12 * math.Pi / 180
	board := func(cx, cy, w, h, r float64) func(float64, float64) bool {
		rr := roundRect(cx, cy, w, h, r)
		return func(x, y float64) bool {
			rx, ry := rot(x, y, 128, 128, -ang)
			return rr(rx, ry)
		}
	}
	shadow := board(128, 134, 178, 178, 40) // 投影（略下移）
	body := board(128, 126, 174, 174, 40)

	// 键帽：两排 4 键 + 空格条（在键盘本地坐标系里定义，再随底板旋转）
	keys := make([]func(float64, float64) bool, 0, 9)
	addKey := func(kx, ky, kw, kh float64) {
		rr := roundRect(kx, ky, kw, kh, kh/2)
		keys = append(keys, func(x, y float64) bool {
			rx, ry := rot(x, y, 128, 128, -ang)
			return rr(rx, ry)
		})
	}
	// 本地坐标（中心 128,126）：两排 4 键，尺寸 26×22，间距 36
	for row := 0; row < 2; row++ {
		for col := 0; col < 4; col++ {
			kx := 128 + (float64(col)-1.5)*36
			ky := 104 + float64(row)*34
			addKey(kx, ky, 26, 22)
		}
	}
	addKey(128, 172, 118, 22) // 空格条

	for y := 0; y < 256; y++ {
		for x := 0; x < 256; x++ {
			var col color.NRGBA
			// 投影层
			if c := coverage(float64(x), float64(y), shadow); c > 0 {
				col = blend(col, color.NRGBA{0, 0, 0, 46}, c)
			}
			// 底板：左上→右下线性渐变
			if c := coverage(float64(x), float64(y), body); c > 0 {
				// 渐变参数：沿旋转后对角线
				rx, ry := rot(float64(x), float64(y), 128, 128, -ang)
				t := clamp01((rx-41)/174*0.5 + (ry-39)/174*0.5)
				base := lerpNRGBA(st.from, st.to, t)
				col = blend(col, base, c)
			} else if col.A == 0 {
				continue // 透明角落，直接下一像素
			}
			// 键帽（白）
			for _, k := range keys {
				if c := coverage(float64(x), float64(y), k); c > 0 {
					col = blend(col, white, c)
					break
				}
			}
			// 键帽底部一点阴影线（键帽下 1-2px 深色描边感，增强立体）
			for _, k := range keys {
				kk := k
				if c := coverage(float64(x), float64(y)-2.5, kk); c > 0 {
					// 只在与键帽不重叠处加深（下边缘）
					if !k(float64(x), float64(y)) {
						col = blend(col, color.NRGBA{0, 0, 0, 26}, c*0.7)
					}
					break
				}
			}
			img.SetNRGBA(x, y, col)
		}
	}
	return img
}

// ---------- 小尺寸（16/24/32px）极简版 ----------

func renderSmall(st stateDef, size int) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	s := float64(size)

	// 底板：几乎占满的圆角方（无旋转：小尺寸旋转只剩锯齿）
	body := roundRect(s/2, s/2, s-2, s-2, s*0.26)

	// 白色键帽：一枚大键 + 空格条（16px 下两排键必糊，极简到两元素）
	pad := s * 0.22
	keyW := s - 2*pad
	key := roundRect(s/2, s*0.40, keyW, s*0.22, s*0.07)
	bar := roundRect(s/2, s*0.68, keyW, s*0.16, s*0.06)

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var col color.NRGBA
			if c := coverage(float64(x), float64(y), body); c > 0 {
				t := float64(y) / s
				col = blend(col, lerpNRGBA(st.from, st.to, t), c)
			} else {
				continue
			}
			if c := coverage(float64(x), float64(y), key); c > 0 {
				col = blend(col, color.NRGBA{255, 255, 255, 255}, c)
			}
			if c := coverage(float64(x), float64(y), bar); c > 0 {
				col = blend(col, color.NRGBA{255, 255, 255, 255}, c)
			}
			img.SetNRGBA(x, y, col)
		}
	}
	return img
}

// ---------- 颜色工具 ----------

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func lerpNRGBA(a, b color.NRGBA, t float64) color.NRGBA {
	t = clamp01(t)
	return color.NRGBA{
		R: uint8(float64(a.R) + (float64(b.R)-float64(a.R))*t),
		G: uint8(float64(a.G) + (float64(b.G)-float64(a.G))*t),
		B: uint8(float64(a.B) + (float64(b.B)-float64(a.B))*t),
		A: 255,
	}
}

// blend 按 c∈[0,1] 把 src 混到 dst 上（dst.A==0 时直接取 src）。
func blend(dst, src color.NRGBA, c float64) color.NRGBA {
	if c <= 0 {
		return dst
	}
	if c > 1 {
		c = 1
	}
	af := float64(src.A) / 255 * c
	if dst.A == 0 {
		out := color.NRGBA{
			R: uint8(float64(src.R)*af + float64(dst.R)*(1-af)),
			G: uint8(float64(src.G)*af + float64(dst.G)*(1-af)),
			B: uint8(float64(src.B)*af + float64(dst.B)*(1-af)),
		}
		out.A = uint8(af * 255)
		// 直通时保留原色
		if af >= 1 {
			return color.NRGBA{R: src.R, G: src.G, B: src.B, A: 255}
		}
		return out
	}
	return color.NRGBA{
		R: uint8(float64(src.R)*af + float64(dst.R)*(1-af)),
		G: uint8(float64(src.G)*af + float64(dst.G)*(1-af)),
		B: uint8(float64(src.B)*af + float64(dst.B)*(1-af)),
		A: 255,
	}
}
