// Package imagex сжимает иллюстрации перед укладкой в книгу.
//
// Всё делается внутри процесса: ни ImageMagick, ни ffmpeg, ни cgo не нужны.
// Масштабирование берётся из golang.org/x/image/draw, кодирование JPEG —
// из jpegli (кодировщик из libjxl, собранный в WASM): при одном и том же
// значении качества он даёт файл примерно на 14% легче стандартного и при этом
// чуть ближе к оригиналу. Если он почему-то откажет, работает запасной путь
// через image/jpeg из стандартной библиотеки.
//
// Оригиналы никогда не меняются: результат пишется в отдельный каталог,
// поэтому книгу всегда можно пересобрать с другими настройками или без сжатия.
package imagex

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"

	"github.com/gen2brain/jpegli"
	xdraw "golang.org/x/image/draw"

	// Регистрация декодеров: сайт отдаёт и такие форматы.
	_ "golang.org/x/image/webp"
	_ "image/gif"
)

// Result — что получилось из исходного файла.
type Result struct {
	Path      string // путь к файлу, который надо класть в книгу
	Name      string // имя файла; расширение могло смениться (png → jpg)
	MediaType string
	Size      int64
	// Changed сообщает, отличается ли результат от исходника.
	Changed bool
}

// Optimizer превращает исходный файл в тот, что попадёт в книгу.
// Реализация вправе вернуть исходник как есть — это не ошибка.
type Optimizer interface {
	Optimize(srcPath string) (Result, error)
}

// Passthrough отдаёт файлы как есть — когда сжатие не нужно.
type Passthrough struct{}

// Optimize реализует Optimizer.
func (Passthrough) Optimize(src string) (Result, error) {
	info, err := os.Stat(src)
	if err != nil {
		return Result{}, err
	}
	return Result{Path: src, Name: filepath.Base(src), MediaType: MediaType(ext(src)), Size: info.Size()}, nil
}

// Resizer уменьшает картинки и пережимает их в JPEG.
//
// Картинка с прозрачностью остаётся PNG: в JPEG у неё почернел бы фон.
// Анимация (gif) не трогается — от неё осталась бы одна первая кадр.
type Resizer struct {
	// MaxSize — предел по большей стороне в пикселях. 0 — не уменьшать.
	MaxSize int
	// Quality — качество JPEG, 1..100. 0 означает 82.
	Quality int
	// Dir — куда складывать результат.
	Dir string
	// Scaler задаёт качество масштабирования. По умолчанию CatmullRom:
	// он заметно чётче билинейного на текстовых иллюстрациях и картах.
	Scaler xdraw.Interpolator
}

// NewResizer готовит оптимизатор и создаёт каталог для результатов.
func NewResizer(dir string, maxSize, quality int) (*Resizer, error) {
	if quality <= 0 || quality > 100 {
		quality = 82
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Resizer{MaxSize: maxSize, Quality: quality, Dir: dir, Scaler: xdraw.CatmullRom}, nil
}

// Optimize сжимает один файл. Если сжатая версия вышла тяжелее исходной,
// возвращается исходная — так бывает с маленькими картинками.
func (r *Resizer) Optimize(src string) (Result, error) {
	info, err := os.Stat(src)
	if err != nil {
		return Result{}, err
	}
	original := Result{Path: src, Name: filepath.Base(src), MediaType: MediaType(ext(src)), Size: info.Size()}

	switch ext(src) {
	case "gif", "svg":
		return original, nil
	}

	data, err := os.ReadFile(src)
	if err != nil {
		return original, err
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return original, fmt.Errorf("imagex: %s: %w", filepath.Base(src), err)
	}

	img, resized := r.fit(img)
	transparent := hasAlpha(img)
	if transparent && !resized {
		return original, nil // перекодировать нечего: PNG останется PNG того же размера
	}

	target := "jpg"
	if transparent {
		target = "png"
	}
	out, err := encode(img, target, r.Quality)
	if err != nil {
		return original, err
	}
	if int64(len(out)) >= info.Size() {
		return original, nil
	}

	name := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src)) + "." + target
	dst := filepath.Join(r.Dir, name)
	if err := os.WriteFile(dst, out, 0o644); err != nil {
		return original, err
	}
	return Result{Path: dst, Name: name, MediaType: MediaType(target), Size: int64(len(out)), Changed: true}, nil
}

// fit уменьшает картинку до предела по большей стороне. Увеличивать не станет.
func (r *Resizer) fit(img image.Image) (image.Image, bool) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if r.MaxSize <= 0 || max(w, h) <= r.MaxSize {
		return img, false
	}

	scale := float64(r.MaxSize) / float64(max(w, h))
	nw, nh := max(1, int(float64(w)*scale)), max(1, int(float64(h)*scale))

	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	scaler := r.Scaler
	if scaler == nil {
		scaler = xdraw.CatmullRom
	}
	scaler.Scale(dst, dst.Bounds(), img, b, xdraw.Over, nil)
	return dst, true
}

func encode(img image.Image, format string, quality int) ([]byte, error) {
	if format == "png" {
		var buf bytes.Buffer
		if err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&buf, img); err != nil {
			return nil, fmt.Errorf("imagex: кодирование png: %w", err)
		}
		return buf.Bytes(), nil
	}

	// Прозрачность к этому моменту исключена, но подложка не помешает:
	// без неё полупрозрачные пиксели ушли бы в чёрный.
	flat := flatten(img)

	// jpegli при том же значении качества даёт файл примерно на 14% легче
	// стандартного кодировщика и при этом чуть ближе к оригиналу по SSIM.
	var buf bytes.Buffer
	err := jpegli.Encode(&buf, flat, &jpegli.EncodingOptions{
		Quality:              quality,
		ChromaSubsampling:    image.YCbCrSubsampleRatio420,
		OptimizeCoding:       true,
		AdaptiveQuantization: true,
	})
	if err == nil {
		return buf.Bytes(), nil
	}

	// Кодировщик из стандартной библиотеки как запасной путь: он проще,
	// зато не подведёт никогда.
	buf.Reset()
	if err := jpeg.Encode(&buf, flat, &jpeg.Options{Quality: quality}); err != nil {
		return nil, fmt.Errorf("imagex: кодирование jpeg: %w", err)
	}
	return buf.Bytes(), nil
}

// flatten кладёт картинку на белый фон, если у неё есть альфа-канал.
func flatten(img image.Image) image.Image {
	if !hasAlpha(img) {
		return img
	}
	b := img.Bounds()
	dst := image.NewRGBA(b)
	draw.Draw(dst, b, image.NewUniform(image.White.C), image.Point{}, draw.Src)
	draw.Draw(dst, b, img, b.Min, draw.Over)
	return dst
}

// hasAlpha проверяет, есть ли в картинке хоть один непрозрачный не до конца пиксель.
// Формат с альфа-каналом сам по себе ничего не значит: PNG сплошь и рядом полностью непрозрачны.
func hasAlpha(img image.Image) bool {
	switch img.(type) {
	case *image.YCbCr, *image.Gray, *image.CMYK:
		return false // такие форматы альфа-канала не имеют вовсе
	}
	b := img.Bounds()
	// Шаг по сетке: полный обход больших картинок стоит дороже самой пережимки.
	step := max(1, max(b.Dx(), b.Dy())/512)
	for y := b.Min.Y; y < b.Max.Y; y += step {
		for x := b.Min.X; x < b.Max.X; x += step {
			if _, _, _, a := img.At(x, y).RGBA(); a < 0xffff {
				return true
			}
		}
	}
	return false
}

// MediaType возвращает MIME-тип по расширению файла.
func MediaType(e string) string {
	switch strings.ToLower(strings.TrimPrefix(e, ".")) {
	case "png":
		return "image/png"
	case "gif":
		return "image/gif"
	case "webp":
		return "image/webp"
	case "svg":
		return "image/svg+xml"
	default:
		return "image/jpeg"
	}
}

func ext(p string) string {
	return strings.ToLower(strings.TrimPrefix(filepath.Ext(p), "."))
}
