package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"time"
)

// appIconPNG 纯代码生成托盘图标（圆角方形 + 浅色闸门意象），无需外部资源。
//
// 注意：同目录 appicon.ico（窗口/任务栏图标，经 .syso 打进 exe）必须与本函数的
// 比例保持一致，否则托盘与窗口图标看起来会是两个样子。
// 共享规格（以边长 S 为基准）：圆角 0.22S；竖条宽 0.1875S，x 起点 0.21875S / 0.59375S，
// y 0.25S..0.75S；横杠 x 0.21875S、y 0.40625S、宽 0.5625S、高 0.1875S；底色 #3A3A42→#18181B。
func appIconPNG() []byte {
	const size = 64
	const radius = size * 0.22
	const ss = 3 // 每像素超采样数，用于抗锯齿

	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var cover, r, g, b float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					fx := float64(x) + (float64(sx)+0.5)/ss
					fy := float64(y) + (float64(sy)+0.5)/ss
					if !insideRounded(fx, fy, size, size, radius) {
						continue
					}
					cover++
					if gateGlyph(fx, fy, size) {
						// 中央浅色「闸门」符号
						r += 0xEC
						g += 0xEC
						b += 0xF0
						continue
					}
					// 竖向渐变：深灰 -> 近黑
					t := fy / size
					r += 0x3A + (0x18-0x3A)*t
					g += 0x3A + (0x18-0x3A)*t
					b += 0x42 + (0x1B-0x42)*t
				}
			}
			n := float64(ss * ss)
			img.SetRGBA(x, y, color.RGBA{
				R: uint8(r / n), G: uint8(g / n), B: uint8(b / n),
				A: uint8(cover / n * 255),
			})
		}
	}

	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// gateGlyph 描述图标中央的浅色符号：左右两根竖条 + 中间横杠。
func gateGlyph(x, y float64, size float64) bool {
	s := size / 64.0
	// 左竖条
	if insideRounded(x-14*s, y-16*s, 12*s, 32*s, 4*s) {
		return true
	}
	// 右竖条
	if insideRounded(x-38*s, y-16*s, 12*s, 32*s, 4*s) {
		return true
	}
	// 中间横杠（与竖条垂直居中：y 26..38，共 64）
	if insideRounded(x-14*s, y-26*s, 36*s, 12*s, 5*s) {
		return true
	}
	return false
}

// insideRounded 判断点 (x,y) 是否落在左上角为 (0,0)、宽高为 (w,h)、圆角 r 的圆角矩形内。
func insideRounded(x, y, w, h, r float64) bool {
	if x < 0 || y < 0 || x > w || y > h {
		return false
	}
	// 四个角的圆形区域
	cx, cy := r, r
	switch {
	case x < r && y < r:
		cx, cy = r, r
	case x > w-r && y < r:
		cx, cy = w-r, r
	case x < r && y > h-r:
		cx, cy = r, h-r
	case x > w-r && y > h-r:
		cx, cy = w-r, h-r
	default:
		return true
	}
	dx, dy := x-cx, y-cy
	return dx*dx+dy*dy <= r*r
}

func nowClock() string {
	return time.Now().Format("15:04:05")
}