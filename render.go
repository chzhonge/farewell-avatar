package main

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"math"
	"math/rand"
	"strconv"
)

const avatarSize = 640

func decodeSource(data []byte) (image.Image, error) {
	if len(data) == 0 || len(data) > 10<<20 {
		return nil, errors.New("圖片檔案須小於 10 MB")
	}
	cfg, format, e := image.DecodeConfig(bytes.NewReader(data))
	if e != nil || !(format == "png" || format == "jpeg" || format == "gif") {
		return nil, errors.New("僅支援有效的 PNG、JPEG 或 GIF 圖片")
	}
	if e = validateImageSize(cfg.Width, cfg.Height); e != nil {
		return nil, e
	}
	src, _, e := image.Decode(bytes.NewReader(data))
	if e != nil {
		return nil, errors.New("無法解碼圖片")
	}
	return src, nil
}
func prepareImage(data []byte, crop Crop) (*image.RGBA, error) {
	src, e := decodeSource(data)
	if e != nil {
		return nil, e
	}
	out := image.NewRGBA(image.Rect(0, 0, avatarSize, avatarSize))
	draw.Draw(out, out.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	b := src.Bounds()
	if crop.Mode == "crop" {
		if crop.Zoom < 100 || crop.Zoom > 300 || crop.X < 0 || crop.X > 100 || crop.Y < 0 || crop.Y > 100 {
			return nil, errors.New("無效的裁切設定")
		}
		side := float64(min(w, h)) * 100 / float64(crop.Zoom)
		left := float64(w) - side
		top := float64(h) - side
		left *= float64(crop.X) / 100
		top *= float64(crop.Y) / 100
		for y := 0; y < avatarSize; y++ {
			sy := b.Min.Y + min(h-1, int(top+(float64(y)+0.5)*side/avatarSize))
			for x := 0; x < avatarSize; x++ {
				sx := b.Min.X + min(w-1, int(left+(float64(x)+0.5)*side/avatarSize))
				out.Set(x, y, src.At(sx, sy))
			}
		}
		return out, nil
	}
	scale := math.Min(float64(avatarSize)/float64(w), float64(avatarSize)/float64(h))
	dw, dh := int(math.Round(float64(w)*scale)), int(math.Round(float64(h)*scale))
	ox, oy := (avatarSize-dw)/2, (avatarSize-dh)/2
	for y := 0; y < dh; y++ {
		sy := b.Min.Y + int(float64(y)*float64(h)/float64(dh))
		for x := 0; x < dw; x++ {
			sx := b.Min.X + int(float64(x)*float64(w)/float64(dw))
			out.Set(ox+x, oy+y, src.At(sx, sy))
		}
	}
	return out, nil
}
func normalizeImage(data []byte) ([]byte, error) {
	out, e := prepareImage(data, Crop{Mode: "fit"})
	if e != nil {
		return nil, e
	}
	var buf bytes.Buffer
	if e = png.Encode(&buf, out); e != nil {
		return nil, e
	}
	return buf.Bytes(), nil
}
func parseColor(s string) (color.RGBA, error) {
	if len(s) != 7 || s[0] != '#' {
		return color.RGBA{}, errors.New("invalid color")
	}
	v, e := strconv.ParseUint(s[1:], 16, 24)
	if e != nil {
		return color.RGBA{}, e
	}
	return color.RGBA{uint8(v >> 16), uint8(v >> 8), uint8(v), 255}, nil
}
func renderAvatar(source []byte, s Settings, date string) ([]byte, error) {
	p, remaining, e := dayInfo(s, date)
	if e != nil {
		return nil, e
	}
	src, e := prepareImage(source, s.Crop)
	if e != nil {
		return nil, errors.New("原圖已損壞")
	}
	dst := image.NewRGBA(image.Rect(0, 0, avatarSize, avatarSize))
	draw.Draw(dst, dst.Bounds(), src, src.Bounds().Min, draw.Src)
	if s.Effects.Gray {
		grayLeft(dst, p)
	}
	if s.Effects.Blur && p > 0 {
		blur(dst, int(math.Round(float64(s.Effects.BlurMax)*p)))
	}
	if s.Effects.Pixel && p > 0 {
		block := 1 + int(math.Round(float64(s.Effects.PixelMax-1)*p))
		if block > 1 {
			pixelate(dst, block)
		}
	}
	if s.Effects.Dust {
		dust(dst, p)
	}
	if s.Effects.Ring {
		c, _ := parseColor(s.Effects.RingColor)
		ring(dst, p, c)
	}
	if s.Effects.Badge {
		badge(dst, remaining, s.Effects.BadgePosition)
	}
	var buf bytes.Buffer
	if e = png.Encode(&buf, dst); e != nil {
		return nil, e
	}
	return buf.Bytes(), nil
}
func grayLeft(img *image.RGBA, p float64) {
	limit := int(math.Round(p * avatarSize))
	for y := 0; y < avatarSize; y++ {
		for x := 0; x < limit; x++ {
			i := img.PixOffset(x, y)
			r, g, b := img.Pix[i], img.Pix[i+1], img.Pix[i+2]
			v := uint8((299*int(r) + 587*int(g) + 114*int(b) + 500) / 1000)
			img.Pix[i], img.Pix[i+1], img.Pix[i+2] = v, v, v
		}
	}
}
func blur(img *image.RGBA, r int) {
	if r < 1 {
		return
	}
	copyPix := append([]byte(nil), img.Pix...)
	for y := 0; y < avatarSize; y++ {
		for x := 0; x < avatarSize; x++ {
			var sum [4]int
			n := 0
			for xx := max(0, x-r); xx <= min(avatarSize-1, x+r); xx++ {
				i := 4 * (y*avatarSize + xx)
				for c := 0; c < 4; c++ {
					sum[c] += int(copyPix[i+c])
				}
				n++
			}
			i := 4 * (y*avatarSize + x)
			for c := 0; c < 4; c++ {
				img.Pix[i+c] = uint8(sum[c] / n)
			}
		}
	}
	copy(copyPix, img.Pix)
	for y := 0; y < avatarSize; y++ {
		for x := 0; x < avatarSize; x++ {
			var sum [4]int
			n := 0
			for yy := max(0, y-r); yy <= min(avatarSize-1, y+r); yy++ {
				i := 4 * (yy*avatarSize + x)
				for c := 0; c < 4; c++ {
					sum[c] += int(copyPix[i+c])
				}
				n++
			}
			i := 4 * (y*avatarSize + x)
			for c := 0; c < 4; c++ {
				img.Pix[i+c] = uint8(sum[c] / n)
			}
		}
	}
}
func pixelate(img *image.RGBA, block int) {
	for y := 0; y < avatarSize; y += block {
		for x := 0; x < avatarSize; x += block {
			var sum [4]int
			n := 0
			for yy := y; yy < min(y+block, avatarSize); yy++ {
				for xx := x; xx < min(x+block, avatarSize); xx++ {
					i := img.PixOffset(xx, yy)
					for c := 0; c < 4; c++ {
						sum[c] += int(img.Pix[i+c])
					}
					n++
				}
			}
			var avg [4]uint8
			for c := 0; c < 4; c++ {
				avg[c] = uint8(sum[c] / n)
			}
			for yy := y; yy < min(y+block, avatarSize); yy++ {
				for xx := x; xx < min(x+block, avatarSize); xx++ {
					i := img.PixOffset(xx, yy)
					copy(img.Pix[i:i+4], avg[:])
				}
			}
		}
	}
}
func ring(img *image.RGBA, p float64, c color.RGBA) {
	cx, cy := float64(avatarSize-1)/2, float64(avatarSize-1)/2
	for y := 0; y < avatarSize; y++ {
		for x := 0; x < avatarSize; x++ {
			dx, dy := float64(x)-cx, float64(y)-cy
			d := math.Hypot(dx, dy)
			if d < 300 || d > 313 {
				continue
			}
			angle := math.Atan2(dx, -dy)
			if angle < 0 {
				angle += 2 * math.Pi
			}
			if angle <= 2*math.Pi*p {
				img.SetRGBA(x, y, c)
			} else {
				img.SetRGBA(x, y, color.RGBA{70, 70, 70, 255})
			}
		}
	}
}

var digitFont = map[rune][7]string{
	'0': {"01110", "10001", "10011", "10101", "11001", "10001", "01110"},
	'1': {"00100", "01100", "00100", "00100", "00100", "00100", "01110"},
	'2': {"01110", "10001", "00001", "00010", "00100", "01000", "11111"},
	'3': {"11110", "00001", "00001", "01110", "00001", "00001", "11110"},
	'4': {"00010", "00110", "01010", "10010", "11111", "00010", "00010"},
	'5': {"11111", "10000", "11110", "00001", "00001", "10001", "01110"},
	'6': {"00110", "01000", "10000", "11110", "10001", "10001", "01110"},
	'7': {"11111", "00001", "00010", "00100", "01000", "01000", "01000"},
	'8': {"01110", "10001", "10001", "01110", "10001", "10001", "01110"},
	'9': {"01110", "10001", "10001", "01111", "00001", "00010", "11100"},
}

func badge(img *image.RGBA, remaining int, pos string) {
	label := strconv.Itoa(remaining)
	scale := 7
	if len(label) > 3 {
		scale = 5
	}
	w := len(label)*6*scale + 20
	h := 7*scale + 20
	x, y := 32, 32
	if pos == "top-right" || pos == "bottom-right" {
		x = avatarSize - w - 32
	}
	if pos == "bottom-left" || pos == "bottom-right" {
		y = avatarSize - h - 32
	}
	draw.Draw(img, image.Rect(x, y, x+w, y+h), &image.Uniform{C: color.RGBA{15, 23, 42, 255}}, image.Point{}, draw.Src)
	for k, ch := range label {
		glyph := digitFont[ch]
		for gy, line := range glyph {
			for gx, v := range line {
				if v != '1' {
					continue
				}
				rect := image.Rect(x+10+(k*6+gx)*scale, y+10+gy*scale, x+10+(k*6+gx+1)*scale, y+10+(gy+1)*scale)
				draw.Draw(img, rect, &image.Uniform{C: color.White}, image.Point{}, draw.Src)
			}
		}
	}
}

// Dust uses a fixed seed so fragments follow the same paths across dates and renders.
func dust(img *image.RGBA, p float64) {
	if p <= 0 {
		return
	}
	src := image.NewRGBA(img.Bounds())
	copy(src.Pix, img.Pix)
	draw.Draw(img, img.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	if p >= 1 {
		return
	}
	rng := rand.New(rand.NewSource(1))
	const cell = 4
	for y := 0; y < avatarSize; y += cell {
		for x := 0; x < avatarSize; x += cell {
			start := 0.78*(1-float64(x)/avatarSize) + 0.12*rng.Float64()
			speed, drift := 100+180*rng.Float64(), -80+160*rng.Float64()
			life := (p - start) / 0.22
			rect := image.Rect(x, y, x+cell, y+cell)
			if life <= 0 {
				draw.Draw(img, rect, src, rect.Min, draw.Src)
				continue
			}
			if life >= 1 {
				continue
			}
			size := max(1, int(math.Ceil(cell*(1-life))))
			target := image.Rect(x+int(speed*life), y+int(drift*life), x+int(speed*life)+size, y+int(drift*life)+size)
			mask := image.NewUniform(color.Alpha{A: uint8(255 * (1 - life))})
			draw.DrawMask(img, target, src, rect.Min, mask, image.Point{}, draw.Over)
		}
	}
}
