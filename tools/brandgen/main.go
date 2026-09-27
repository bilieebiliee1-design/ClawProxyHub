// brandgen — NexPort 品牌静态资源生成（logo / favicon / apple-touch-icon）。
// 用法：go run tools/brandgen/main.go dashboard/public
// 一次性工具：画靛蓝渐变圆角方块 + 白色几何 "N"，输出各尺寸 PNG 与 ICO（内嵌 PNG）。
package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
)

// 渐变端色：靛蓝 → 紫罗兰（与面板 TDesign 主色系协调）
var (
	c1 = color.RGBA{R: 0x4F, G: 0x46, B: 0xE5, A: 0xFF} // indigo-600
	c2 = color.RGBA{R: 0x8B, G: 0x5C, B: 0xF6, A: 0xFF} // violet-500
)

func lerp(a, b color.RGBA, t float64) color.RGBA {
	return color.RGBA{
		R: uint8(float64(a.R)*(1-t) + float64(b.R)*t),
		G: uint8(float64(a.G)*(1-t) + float64(b.G)*t),
		B: uint8(float64(a.B)*(1-t) + float64(b.B)*t),
		A: 0xFF,
	}
}

func cornerRadius(x, y, w, h, r int) bool {
	cx, cy := -1, -1
	if x < r && y < r {
		cx, cy = r-x, r-y
	} else if x >= w-r && y < r {
		cx, cy = x-(w-r-1), r-y
	} else if x < r && y >= h-r {
		cx, cy = r-x, y-(h-r-1)
	} else if x >= w-r && y >= h-r {
		cx, cy = x-(w-r-1), y-(h-r-1)
	}
	if cx < 0 {
		return false
	}
	return cx*cx+cy*cy > r*r
}

// drawLogo 画 size×size 的品牌方块与白色 N。
func drawLogo(size int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	r := size * 22 / 100
	if r < 2 {
		r = 2
	}
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			if cornerRadius(x, y, size, size, r) {
				continue
			}
			t := (float64(x) + float64(y)) / (2 * float64(size))
			img.Set(x, y, lerp(c1, c2, t))
		}
	}
	// 白色几何 N：两根竖杠 + 对角杠
	bar := size * 12 / 100
	x0 := size * 26 / 100
	x1 := size * 62 / 100
	yTop := size * 26 / 100
	yBot := size * 74 / 100
	white := color.RGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF}
	fill := func(xa, xb, ya, yb int) {
		for y := ya; y <= yb; y++ {
			for x := xa; x <= xb; x++ {
				if x < 0 || y < 0 || x >= size || y >= size {
					continue
				}
				if cornerRadius(x, y, size, size, r) {
					continue
				}
				img.Set(x, y, white)
			}
		}
	}
	fill(x0, x0+bar-1, yTop, yBot)                 // 左竖
	fill(x1, x1+bar-1, yTop, yBot)                 // 右竖
	// 对角：从左竖顶到右竖底的斜带
	for i := 0; i <= yBot-yTop; i++ {
		yc := yTop + i
		xc := x0 + (x1+bar-1-x0-bar)*i/(yBot-yTop)
		fill(xc, xc+bar-1, yc, yc)
	}
	return img
}

func writePNG(img image.Image, path string) {
	f, err := os.Create(path)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		panic(err)
	}
}

// writeICO ICO 容器：单条目内嵌 PNG（Vista+ 标准，浏览器全兼容）。
func writeICO(img image.Image, path string) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	pngData := buf.Bytes()
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	out := bytes.NewBuffer(nil)
	// ICONDIR
	out.Write([]byte{0, 0, 1, 0, 1, 0})
	// ICONDIRENTRY
	bw, bh := byte(w%256), byte(h%256)
	out.Write([]byte{bw, bh, 0, 0, 1, 0, 32, 0})
	out.Write([]byte{byte(len(pngData)), 0, 0, 0})
	out.Write([]byte{22, 0, 0, 0})
	out.Write(pngData)
	if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
		panic(err)
	}
}

func main() {
	out := os.Args[1]
	for _, spec := range []struct {
		name string
		size int
	}{
		{"logo.png", 256},
		{"logo-mark.png", 128},
		{"logo-512.png", 512},
		{"favicon-32.png", 32},
		{"apple-touch-icon.png", 180},
	} {
		p := filepath.Join(out, spec.name)
		writePNG(drawLogo(spec.size), p)
		fmt.Println("wrote", p)
	}
	writeICO(drawLogo(32), filepath.Join(out, "favicon.ico"))
	fmt.Println("wrote", filepath.Join(out, "favicon.ico"))
}
