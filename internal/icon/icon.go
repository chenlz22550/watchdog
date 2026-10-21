// Package icon 用代码绘制应用图标：圆角渐变底 + 心电图折线 + 状态角标
package icon

import (
	"image"
	"image/color"
	"math"
)

type State int

const (
	Normal State = iota // 全部正常（绿色角标）
	Alert               // 有异常（红色角标）
	Idle                // 无运行程序（无角标）
)

type rgb struct{ r, g, b float64 }

var (
	bgTop    = rgb{99, 102, 241} // #6366F1
	bgBottom = rgb{139, 92, 246} // #8B5CF6
	green    = rgb{34, 197, 94}
	red      = rgb{239, 68, 68}
)

// 心电图折线（以 0..1 归一化坐标）
var pulse = [][2]float64{
	{0.16, 0.54}, {0.34, 0.54}, {0.42, 0.34}, {0.52, 0.74}, {0.60, 0.46}, {0.66, 0.54}, {0.84, 0.54},
}

// Render 绘制 size×size 图标，4×4 超采样抗锯齿
func Render(size int, st State) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	const ss = 4
	fs := float64(size)
	radius := 0.22 * fs
	lineW := math.Max(1.6, fs*0.085)
	badgeR := fs * 0.2
	bx, by := fs-badgeR-0.5, fs-badgeR-0.5

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var ar, ag, ab, aa float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					px := float64(x) + (float64(sx)+0.5)/ss
					py := float64(y) + (float64(sy)+0.5)/ss
					var c rgb
					var a float64
					if roundRectInside(px, py, fs, radius) {
						t := py / fs
						c = rgb{lerp(bgTop.r, bgBottom.r, t), lerp(bgTop.g, bgBottom.g, t), lerp(bgTop.b, bgBottom.b, t)}
						a = 1
						if polylineDist(px/fs, py/fs)*fs <= lineW/2 {
							c = rgb{255, 255, 255}
						}
					}
					if st != Idle {
						d := math.Hypot(px-bx, py-by)
						if d <= badgeR {
							c, a = green, 1
							if st == Alert {
								c = red
							}
						} else if d <= badgeR+fs*0.06 { // 白色描边
							c, a = rgb{255, 255, 255}, 1
						}
					}
					ar += c.r * a
					ag += c.g * a
					ab += c.b * a
					aa += a
				}
			}
			n := float64(ss * ss)
			if aa == 0 {
				continue
			}
			img.SetRGBA(x, y, color.RGBA{
				R: uint8(ar / aa), G: uint8(ag / aa), B: uint8(ab / aa), A: uint8(aa / n * 255),
			})
		}
	}
	return img
}

func lerp(a, b, t float64) float64 { return a + (b-a)*t }

func roundRectInside(x, y, s, r float64) bool {
	cx := math.Max(r, math.Min(s-r, x))
	cy := math.Max(r, math.Min(s-r, y))
	return math.Hypot(x-cx, y-cy) <= r
}

func polylineDist(x, y float64) float64 {
	best := math.MaxFloat64
	for i := 0; i+1 < len(pulse); i++ {
		ax, ay := pulse[i][0], pulse[i][1]
		bx, by := pulse[i+1][0], pulse[i+1][1]
		dx, dy := bx-ax, by-ay
		t := ((x-ax)*dx + (y-ay)*dy) / (dx*dx + dy*dy)
		t = math.Max(0, math.Min(1, t))
		if d := math.Hypot(x-(ax+t*dx), y-(ay+t*dy)); d < best {
			best = d
		}
	}
	return best
}
