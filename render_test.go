package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func testSource(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, avatarSize, avatarSize))
	for y := 0; y < avatarSize; y++ {
		for x := 0; x < avatarSize; x++ {
			img.SetRGBA(x, y, color.RGBA{200, 30, 10, 255})
		}
	}
	var b bytes.Buffer
	if e := png.Encode(&b, img); e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}
func TestDates(t *testing.T) {
	s := defaultSettings()
	s.StartDate = "2026-09-01"
	s.EndDate = "2026-09-05"
	cases := []struct {
		date   string
		p      float64
		remain int
	}{{"2026-08-31", 0, 5}, {"2026-09-01", 0, 4}, {"2026-09-03", .5, 2}, {"2026-09-05", 1, 0}, {"2026-09-06", 1, 0}}
	for _, c := range cases {
		p, n, e := dayInfo(s, c.date)
		if e != nil || p != c.p || n != c.remain {
			t.Fatalf("%s: %v %v %v", c.date, p, n, e)
		}
	}
	s.EndDate = s.StartDate
	for _, c := range []struct {
		d string
		p float64
	}{{"2026-08-31", 0}, {"2026-09-01", 1}, {"2026-09-02", 1}} {
		p, _, _ := dayInfo(s, c.d)
		if p != c.p {
			t.Errorf("same-day %s: %v", c.d, p)
		}
	}
}
func TestGrayLeftToRightAndEffects(t *testing.T) {
	src := testSource(t)
	s := defaultSettings()
	s.StartDate = "2026-09-01"
	s.EndDate = "2026-09-03"
	out, e := renderAvatar(src, s, "2026-09-02")
	if e != nil {
		t.Fatal(e)
	}
	img, e := png.Decode(bytes.NewReader(out))
	if e != nil {
		t.Fatal(e)
	}
	left := color.RGBAModel.Convert(img.At(100, 100)).(color.RGBA)
	right := color.RGBAModel.Convert(img.At(500, 100)).(color.RGBA)
	if left.R != left.G || left.G != left.B || right.R == right.G {
		t.Fatalf("gray direction: left %v right %v", left, right)
	}
	end, _ := renderAvatar(src, s, "2026-09-03")
	full, _ := png.Decode(bytes.NewReader(end))
	c := color.RGBAModel.Convert(full.At(500, 100)).(color.RGBA)
	if c.R != c.G {
		t.Fatal("end not full gray")
	}
	s.Effects.Gray = false
	none, e := renderAvatar(src, s, "2026-09-03")
	if e != nil || !bytes.Equal(src, none) {
		t.Fatal("disabled effects changed output")
	}
	s.Effects.Gray = true
	s.Effects.Blur = true
	s.Effects.BlurMax = 5
	s.Effects.Pixel = true
	s.Effects.PixelMax = 12
	s.Effects.Ring = true
	s.Effects.Badge = true
	combo, e := renderAvatar(src, s, "2026-09-02")
	if e != nil || bytes.Equal(out, combo) {
		t.Fatal("combined effects missing")
	}
	combined, _ := png.Decode(bytes.NewReader(combo))
	ringColor := color.RGBAModel.Convert(combined.At(320, 10)).(color.RGBA)
	if ringColor.R != 0x36 || ringColor.G != 0xc5 {
		t.Fatalf("ring not on top: %v", ringColor)
	}
}
func TestNormalizeNonSquareAndBadFormat(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 100, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 100; x++ {
			img.SetRGBA(x, y, color.RGBA{255, 0, 0, 255})
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	out, e := normalizeImage(b.Bytes())
	if e != nil {
		t.Fatal(e)
	}
	s, e := png.Decode(bytes.NewReader(out))
	if e != nil {
		t.Fatal(e)
	}
	if s.Bounds().Dx() != 640 || s.Bounds().Dy() != 640 {
		t.Fatal("not square")
	}
	if color.RGBAModel.Convert(s.At(1, 320)).(color.RGBA).R != 255 || color.RGBAModel.Convert(s.At(1, 320)).(color.RGBA).G != 255 {
		t.Fatal("expected white letterbox")
	}
	if _, e = normalizeImage([]byte("bad")); e == nil {
		t.Fatal("accepted invalid image")
	}
}

func TestCropPositionAndZoom(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 200, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 200; x++ {
			if x < 100 {
				img.SetRGBA(x, y, color.RGBA{255, 0, 0, 255})
			} else {
				img.SetRGBA(x, y, color.RGBA{0, 0, 255, 255})
			}
		}
	}
	var raw bytes.Buffer
	_ = png.Encode(&raw, img)
	fit, e := prepareImage(raw.Bytes(), Crop{Mode: "fit"})
	if e != nil {
		t.Fatal(e)
	}
	if got := fit.RGBAAt(320, 0); got != (color.RGBA{255, 255, 255, 255}) {
		t.Fatalf("fit should letterbox: %v", got)
	}
	left, e := prepareImage(raw.Bytes(), Crop{Mode: "crop", X: 0, Y: 50, Zoom: 100})
	if e != nil {
		t.Fatal(e)
	}
	right, e := prepareImage(raw.Bytes(), Crop{Mode: "crop", X: 100, Y: 50, Zoom: 100})
	if e != nil {
		t.Fatal(e)
	}
	if got := left.RGBAAt(320, 320); got.R != 255 || got.B != 0 {
		t.Fatalf("left crop %v", got)
	}
	if got := right.RGBAAt(320, 320); got.B != 255 || got.R != 0 {
		t.Fatalf("right crop %v", got)
	}
	zoomed, e := prepareImage(raw.Bytes(), Crop{Mode: "crop", X: 50, Y: 50, Zoom: 200})
	if e != nil {
		t.Fatal(e)
	}
	if got := zoomed.RGBAAt(0, 320); got.R != 255 {
		t.Fatalf("zoom start %v", got)
	}
	if got := zoomed.RGBAAt(639, 320); got.B != 255 {
		t.Fatalf("zoom end %v", got)
	}
}
