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

// samplePNG рисует картинку, похожую на настоящую иллюстрацию: плавные переходы
// с мелким шумом. Ровный узор не годится — такой PNG сжимает лучше, чем JPEG,
// и проверять было бы нечего.
func samplePNG(t *testing.T, path string, w, h int, alpha bool) {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	seed := uint32(12345) // шум детерминированный: тест должен быть повторяемым
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

// flatPNG рисует однотонную картинку.
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
		t.Fatal("картинка должна была пережаться")
	}
	if filepath.Ext(res.Name) != ".jpg" {
		t.Errorf("непрозрачная картинка должна стать jpeg, а стала %s", res.Name)
	}
	info, _ := os.Stat(src)
	if res.Size >= info.Size() {
		t.Errorf("результат не легче исходника: %d против %d", res.Size, info.Size())
	}

	f, err := os.Open(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cfg, format, err := image.DecodeConfig(f)
	if err != nil {
		t.Fatalf("результат не читается: %v", err)
	}
	if format != "jpeg" {
		t.Errorf("формат результата: %s", format)
	}
	if max(cfg.Width, cfg.Height) != 1200 {
		t.Errorf("размер после уменьшения: %dx%d", cfg.Width, cfg.Height)
	}
}

// Прозрачную картинку в jpeg переводить нельзя — фон стал бы чёрным.
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
		t.Errorf("картинка с прозрачностью должна остаться png, а стала %s", res.Name)
	}
}

// Если пережатая версия выходит тяжелее исходной, берётся исходная.
// Ровная заливка — как раз такой случай: PNG хранит её в сотне байт.
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
		t.Errorf("ожидался исходник как есть, получено %+v", res)
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
		t.Errorf("Passthrough не должен ничего менять: %+v", res)
	}
}
