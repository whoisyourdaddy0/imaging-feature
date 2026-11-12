package imaging

import (
	"image"
	"image/color"
	"testing"
)

func TestSRGBLUTRoundTrip(t *testing.T) {
	// Every 8-bit sRGB value must survive the sRGB -> linear -> sRGB
	// round trip through the lookup tables unchanged.
	for v := 0; v < 256; v++ {
		got := linearToSrgb8(srgbToLinearLUT[v])
		if got != uint8(v) {
			t.Fatalf("round trip failed for %d: got %d", v, got)
		}
	}
}

func TestLinearLightOptionsComposition(t *testing.T) {
	f := LinearLight(Lanczos)
	if !f.linearLight || f.premultiplyAlpha {
		t.Fatalf("LinearLight: got flags (%v, %v)", f.linearLight, f.premultiplyAlpha)
	}
	f = PremultipliedAlpha(Lanczos)
	if f.linearLight || !f.premultiplyAlpha {
		t.Fatalf("PremultipliedAlpha: got flags (%v, %v)", f.linearLight, f.premultiplyAlpha)
	}
	f = LinearLight(PremultipliedAlpha(Lanczos))
	if !f.linearLight || !f.premultiplyAlpha {
		t.Fatalf("combined: got flags (%v, %v)", f.linearLight, f.premultiplyAlpha)
	}
	// The base filter must not be mutated.
	if Lanczos.linearLight || Lanczos.premultiplyAlpha {
		t.Fatalf("base filter Lanczos was mutated")
	}
	// Kernel and support must be preserved.
	if f.Support != Lanczos.Support {
		t.Fatalf("support changed: got %f want %f", f.Support, Lanczos.Support)
	}
	if f.Kernel(0.5) != Lanczos.Kernel(0.5) {
		t.Fatalf("kernel changed")
	}
}

func solidNRGBA(w, h int, c color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i+0] = c.R
		img.Pix[i+1] = c.G
		img.Pix[i+2] = c.B
		img.Pix[i+3] = c.A
	}
	return img
}

func checkSolidColor(t *testing.T, img *image.NRGBA, c color.NRGBA, delta int) {
	t.Helper()
	for i := 0; i < len(img.Pix); i += 4 {
		if absint(int(img.Pix[i+0])-int(c.R)) > delta ||
			absint(int(img.Pix[i+1])-int(c.G)) > delta ||
			absint(int(img.Pix[i+2])-int(c.B)) > delta ||
			absint(int(img.Pix[i+3])-int(c.A)) > delta {
			t.Fatalf("pixel %d: got %v want %v (delta %d)",
				i/4, img.Pix[i:i+4], c, delta)
		}
	}
}

func TestLinearLightSolidColor(t *testing.T) {
	colors := []color.NRGBA{
		{0, 0, 0, 255},
		{255, 255, 255, 255},
		{10, 128, 250, 255},
		{1, 2, 3, 255},
		{200, 60, 90, 200},
		{255, 0, 0, 128},
	}
	filters := []struct {
		name string
		f    ResampleFilter
	}{
		{"LinearLight(Lanczos)", LinearLight(Lanczos)},
		{"LinearLight(PremultipliedAlpha(Lanczos))", LinearLight(PremultipliedAlpha(Lanczos))},
		{"LinearLight(Box)", LinearLight(Box)},
	}
	for _, c := range colors {
		src := solidNRGBA(64, 64, c)
		for _, tc := range filters {
			t.Run(tc.name, func(t *testing.T) {
				got := Resize(src, 23, 17, tc.f)
				checkSolidColor(t, got, c, 1)
			})
		}
	}
}

func TestLinearLightMidtone(t *testing.T) {
	// Opaque black next to opaque white, downscaled 2x1 -> 1x1 with the Box
	// filter. The correct result is the average in linear light:
	// srgb(0.5) = 0.7354 -> 188. The default sRGB-space path averages the
	// encoded values and produces 128.
	src := &image.NRGBA{
		Rect:   image.Rect(0, 0, 2, 1),
		Stride: 2 * 4,
		Pix: []uint8{
			0x00, 0x00, 0x00, 0xff, 0xff, 0xff, 0xff, 0xff,
		},
	}

	got := Resize(src, 1, 1, LinearLight(Box))
	want := uint8(188) // linear-light average of 0 and 1, re-encoded to sRGB
	for i := 0; i < 3; i++ {
		if absint(int(got.Pix[i])-int(want)) > 1 {
			t.Fatalf("linear-light average: got %d want %d±1", got.Pix[i], want)
		}
	}
	if got.Pix[3] != 0xff {
		t.Fatalf("alpha: got %d want 255", got.Pix[3])
	}

	gotDefault := Resize(src, 1, 1, Box)
	if gotDefault.Pix[0] != 128 {
		t.Fatalf("default path changed: got %d want 128", gotDefault.Pix[0])
	}
}

func TestPremultipliedAlphaNoDarkEdge(t *testing.T) {
	// Opaque red next to fully transparent black, downscaled 2x1 -> 1x1.
	// With premultiplied alpha the transparent pixel contributes no color:
	// the result is half-transparent pure red (255, 0, 0, 128). Without
	// premultiplication the black of the transparent pixel leaks in and
	// darkens the result.
	src := &image.NRGBA{
		Rect:   image.Rect(0, 0, 2, 1),
		Stride: 2 * 4,
		Pix: []uint8{
			0xff, 0x00, 0x00, 0xff, 0x00, 0x00, 0x00, 0x00,
		},
	}

	got := Resize(src, 1, 1, LinearLight(PremultipliedAlpha(Box)))
	want := []uint8{0xff, 0x00, 0x00, 0x80}
	for i := 0; i < 4; i++ {
		if absint(int(got.Pix[i])-int(want[i])) > 1 {
			t.Fatalf("premultiplied: got %v want %v (±1)", got.Pix, want)
		}
	}

	// LinearLight alone does not premultiply: the transparent pixel's black
	// leaks into the red channel (linear average 0.5 -> sRGB 188).
	gotNoPremul := Resize(src, 1, 1, LinearLight(Box))
	if gotNoPremul.Pix[0] >= got.Pix[0] {
		t.Fatalf("expected darkening without PremultipliedAlpha: got r=%d, premultiplied r=%d",
			gotNoPremul.Pix[0], got.Pix[0])
	}
	if absint(int(gotNoPremul.Pix[0])-188) > 1 {
		t.Fatalf("non-premultiplied red: got %d want 188±1", gotNoPremul.Pix[0])
	}
	if gotNoPremul.Pix[3] != 0x80 {
		t.Fatalf("non-premultiplied alpha: got %d want 128", gotNoPremul.Pix[3])
	}
}

func TestLinearLightInputFormats(t *testing.T) {
	c := color.NRGBA{10, 128, 250, 255}
	gray := uint8(128)

	nrgba64 := image.NewNRGBA64(image.Rect(0, 0, 32, 32))
	rgba := image.NewRGBA(image.Rect(0, 0, 32, 32))
	rgba64 := image.NewRGBA64(image.Rect(0, 0, 32, 32))
	g := image.NewGray(image.Rect(0, 0, 32, 32))
	g16 := image.NewGray16(image.Rect(0, 0, 32, 32))
	pal := image.NewPaletted(image.Rect(0, 0, 32, 32), color.Palette{c})
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			nrgba64.SetNRGBA64(x, y, color.NRGBA64{
				R: uint16(c.R) * 0x101, G: uint16(c.G) * 0x101,
				B: uint16(c.B) * 0x101, A: 0xffff,
			})
			rgba.SetRGBA(x, y, color.RGBA{R: c.R, G: c.G, B: c.B, A: 255})
			rgba64.SetRGBA64(x, y, color.RGBA64{
				R: uint16(c.R) * 0x101, G: uint16(c.G) * 0x101,
				B: uint16(c.B) * 0x101, A: 0xffff,
			})
			g.SetGray(x, y, color.Gray{Y: gray})
			g16.SetGray16(x, y, color.Gray16{Y: uint16(gray) * 0x101})
			pal.SetColorIndex(x, y, 0)
		}
	}

	filter := LinearLight(PremultipliedAlpha(Lanczos))

	colorImages := map[string]image.Image{
		"NRGBA":    solidNRGBA(32, 32, c),
		"NRGBA64":  nrgba64,
		"RGBA":     rgba,
		"RGBA64":   rgba64,
		"Paletted": pal,
	}
	for name, img := range colorImages {
		t.Run(name, func(t *testing.T) {
			got := Resize(img, 13, 11, filter)
			checkSolidColor(t, got, c, 1)
		})
	}

	grayC := color.NRGBA{gray, gray, gray, 255}
	grayImages := map[string]image.Image{
		"Gray":   g,
		"Gray16": g16,
	}
	for name, img := range grayImages {
		t.Run(name, func(t *testing.T) {
			got := Resize(img, 13, 11, filter)
			checkSolidColor(t, got, grayC, 1)
		})
	}
}

func TestLinearLightTranslucentRGBA(t *testing.T) {
	// A premultiplied RGBA source: (128, 0, 0, 128) unpremultiplies to
	// (255, 0, 0, 128). A solid image must keep its color when resized.
	src := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for i := 0; i < len(src.Pix); i += 4 {
		src.Pix[i+0] = 128
		src.Pix[i+1] = 0
		src.Pix[i+2] = 0
		src.Pix[i+3] = 128
	}
	got := Resize(src, 15, 9, LinearLight(PremultipliedAlpha(Lanczos)))
	checkSolidColor(t, got, color.NRGBA{255, 0, 0, 128}, 1)
}

func TestLinearLightFitFillThumbnail(t *testing.T) {
	c := color.NRGBA{10, 128, 250, 255}
	filter := LinearLight(PremultipliedAlpha(Lanczos))

	src := solidNRGBA(200, 100, c)

	got := Fit(src, 50, 50, filter)
	if got.Bounds().Dx() != 50 || got.Bounds().Dy() != 25 {
		t.Fatalf("Fit: got size %v", got.Bounds())
	}
	checkSolidColor(t, got, c, 1)

	got = Fill(src, 50, 50, Center, filter)
	if got.Bounds().Dx() != 50 || got.Bounds().Dy() != 50 {
		t.Fatalf("Fill: got size %v", got.Bounds())
	}
	checkSolidColor(t, got, c, 1)

	got = Thumbnail(src, 50, 50, filter)
	if got.Bounds().Dx() != 50 || got.Bounds().Dy() != 50 {
		t.Fatalf("Thumbnail: got size %v", got.Bounds())
	}
	checkSolidColor(t, got, c, 1)

	// Small source image: Fill takes the resizeAndCrop path.
	small := solidNRGBA(60, 60, c)
	got = Fill(small, 30, 30, Center, filter)
	checkSolidColor(t, got, c, 1)
}

func TestLinearLightDefaultPathUnchanged(t *testing.T) {
	// PremultipliedAlpha without LinearLight is routed to the default path
	// and must produce exactly the same bytes as the plain filter.
	src := solidNRGBA(32, 32, color.NRGBA{200, 60, 90, 200})
	// Add some variation so the convolution actually mixes values.
	src.SetNRGBA(10, 10, color.NRGBA{0, 0, 0, 0})
	src.SetNRGBA(20, 15, color.NRGBA{255, 255, 255, 255})

	a := Resize(src, 17, 9, Lanczos)
	b := Resize(src, 17, 9, PremultipliedAlpha(Lanczos))
	if !compareNRGBA(a, b, 0) {
		t.Fatalf("PremultipliedAlpha-only output differs from default path")
	}
}

func TestLinearLightGolden(t *testing.T) {
	got := Resize(testdataBranchesPNG, 150, 0, LinearLight(PremultipliedAlpha(Lanczos)))
	want, err := Open("testdata/out_resize_linear_light.png")
	if err != nil {
		t.Fatalf("failed to open image: %v", err)
	}
	if !compareNRGBAGolden(got, toNRGBA(want)) {
		t.Fatalf("resulting image differs from golden: testdata/out_resize_linear_light.png")
	}
}

func BenchmarkResizeLinearLight(b *testing.B) {
	filters := []struct {
		name string
		f    ResampleFilter
	}{
		{"Default", Lanczos},
		{"LinearLight", LinearLight(Lanczos)},
		{"LinearLightPremultipliedAlpha", LinearLight(PremultipliedAlpha(Lanczos))},
	}
	for _, dir := range []struct {
		name string
		size int
	}{
		{"Down", 100},
		{"Up", 1000},
	} {
		for _, tc := range filters {
			b.Run(dir.name+" Lanczos PNG "+tc.name, func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					Resize(testdataBranchesPNG, dir.size, dir.size, tc.f)
				}
			})
		}
	}
}
