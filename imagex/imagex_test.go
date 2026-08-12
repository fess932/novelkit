package imagex_test

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/fess932/novelkit/imagex"
)

// samplePNG draws something like a real illustration: smooth gradients with a
// little noise. A clean pattern would not do — PNG compresses that better than
// JPEG does, leaving nothing to check.
func samplePNG(t *testing.T, path string, w, h int, alpha bool) {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	seed := uint32(12345) // deterministic noise: the test must be repeatable
	for y := range h {
		for x := range w {
			seed = seed*1664525 + 1013904223
			noise := int(seed>>24) % 24

			a := uint8(255)
			if alpha && x < w/2 {
				a = 128
			}
			img.Set(x, y, color.NRGBA{
				R: clamp(x*255/w + noise),
				G: clamp(y*255/h + noise/2),
				B: clamp((x+y)*255/(w+h) + noise),
				A: a,
			})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

// flatPNG draws a single-colour picture.
func flatPNG(t *testing.T, path string, w, h int) {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.NRGBA{R: 200, G: 40, B: 60, A: 255})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

func clamp(v int) uint8 {
	return uint8(min(max(v, 0), 255))
}

func TestResizerShrinksAndConverts(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "big.png")
	samplePNG(t, src, 2400, 1600, false)

	r, err := imagex.NewResizer(filepath.Join(dir, "min"), 1200, 82)
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Optimize(src)
	if err != nil {
		t.Fatal(err)
	}

	if !res.Changed {
		t.Fatal("the picture should have been re-compressed")
	}
	if filepath.Ext(res.Name) != ".jpg" {
		t.Errorf("an opaque picture should become jpeg, got %s", res.Name)
	}
	info, _ := os.Stat(src)
	if res.Size >= info.Size() {
		t.Errorf("the result is not lighter than the source: %d vs %d", res.Size, info.Size())
	}

	f, err := os.Open(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cfg, format, err := image.DecodeConfig(f)
	if err != nil {
		t.Fatalf("the result does not decode: %v", err)
	}
	if format != "jpeg" {
		t.Errorf("result format: %s", format)
	}
	if max(cfg.Width, cfg.Height) != 1200 {
		t.Errorf("size after scaling: %dx%d", cfg.Width, cfg.Height)
	}
}

// A transparent picture must not become jpeg: its background would turn black.
func TestTransparentStaysPNG(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "alpha.png")
	samplePNG(t, src, 2000, 1400, true)

	r, err := imagex.NewResizer(filepath.Join(dir, "min"), 1200, 82)
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Optimize(src)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Ext(res.Name) != ".png" {
		t.Errorf("a transparent picture should stay png, got %s", res.Name)
	}
}

// When the compressed version comes out heavier, the original is kept.
// A flat fill is exactly that case: PNG stores it in a hundred bytes.
func TestKeepsOriginalWhenCompressionLoses(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "flat.png")
	flatPNG(t, src, 64, 64)

	r, err := imagex.NewResizer(filepath.Join(dir, "min"), 1200, 82)
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Optimize(src)
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed || res.Path != src {
		t.Errorf("expected the untouched original, got %+v", res)
	}
}

func TestPassthrough(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "x.png")
	samplePNG(t, src, 100, 100, false)

	res, err := imagex.Passthrough{}.Optimize(src)
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != src || res.Changed {
		t.Errorf("Passthrough must change nothing: %+v", res)
	}
}
