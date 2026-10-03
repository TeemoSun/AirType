//go:build windows

package ui

import (
	"github.com/lxn/walk"

	"github.com/TeemoSun/AirType/internal/win"
)

// 材质模式共享脚手架（方案 §10.7）：Win11 22H2+ 且 D2D 可用时，窗口启用
// DWM 系统材质（Mica=主窗语义 / Acrylic=瞬态窗语义），**全部可见内容**由
// 单一全面积自绘控件经 D2D 绘制——GDI 像素 alpha=0，在材质上透明；D2D
// 像素可见。任一环节不可用即回退 P1 实色模式，功能零损失。

// materialOK 是否满足材质模式的系统条件（具体窗口还要看渲染器创建成败）。
func materialOK() bool {
	return win.HasSystemBackdrop() && win.D2DAvailable()
}

// matCanvas 材质窗口的绘制画布：全面积自绘控件 + HwndRenderTarget。
// paint 回调在 UI 线程执行，坐标为 96dpi 逻辑单位（D2D RT 自动按窗口
// DPI 缩放）。
type matCanvas struct {
	r      *win.D2DRenderer
	w      *walk.CustomWidget
	target uintptr // 渲染目标 HWND（顶层窗口；子窗口直绘会设备丢失）
	hst    hoverState
	pal    Palette
	onErr  func(error) // 渲染器创建失败回调（日志用，可空）
}

// newMatCanvas 在 host（须铺满窗口）内挂画布。渲染器在首次绘制时惰性
// 创建（此刻窗口已可见、客户区尺寸真实）；创建失败由调用方决定回退。
func newMatCanvas(host walk.Container, pal Palette, paint func(r *win.D2DRenderer, w, h float32) error) (*matCanvas, error) {
	m := &matCanvas{pal: pal}
	var bgErr error
	cw, err := walk.NewCustomWidgetPixels(host, 0, func(canvas *walk.Canvas, bounds walk.Rectangle) error {
		if m.r == nil && m.target != 0 {
			pw, ph := win.ClientRectPixels(m.target)
			r, err := win.NewD2DRenderer(m.target, pw, ph)
			if err != nil {
				if bgErr == nil {
					bgErr = err
					if m.onErr != nil {
						m.onErr(err)
					}
				}
			} else {
				m.r = r
			}
		}
		if m.r == nil {
			// 渲染器不可用：铺实色底（不留白板），正常路径不受影响
			bg, err := walk.NewSolidColorBrush(pal.Window)
			if err == nil {
				defer bg.Dispose()
				_ = canvas.FillRectanglePixels(bg, bounds)
			}
			return nil
		}
		w, h := m.r.Size()
		return paint(m.r, w, h)
	})
	if err != nil {
		return nil, err
	}
	m.w = cw
	if _, err := newWidgetBrush(cw, pal.Window); err != nil {
		return nil, err
	}
	return m, nil
}

// redraw 请求重绘（须在 UI 线程）。
func (m *matCanvas) redraw() {
	if m != nil && m.w != nil {
		m.w.Invalidate()
	}
}

// focus 让画布持有键盘焦点（滚轮/按键事件发往画布）。
func (m *matCanvas) focus() {
	_ = m.w.SetFocus()
}

// trackHover 鼠标进入时请求离开通知，返回当前悬停态（供绘制）。
func (m *matCanvas) trackHover(onLeave func()) bool {
	m.hst.attach(m.w, onLeave)
	return true
}

// enableMaterialBackdrop 给无边框窗口启用瞬态材质（Acrylic）。
func enableMaterialBackdrop(hwnd uintptr, transient bool) {
	win.EnableSystemBackdrop(hwnd, transient)
}
