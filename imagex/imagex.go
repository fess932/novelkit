// Package imagex shrinks illustrations before they go into a book.
//
// Everything happens in-process: no ImageMagick, no ffmpeg, no cgo. Scaling
// comes from golang.org/x/image/draw and JPEG encoding from jpegli (the libjxl
// encoder compiled to WASM), which at the same quality setting produces files
// about 14% smaller than image/jpeg while staying slightly closer to the original.
//
// Originals are never modified: results are written to a separate directory, so
// a book can always be rebuilt with different settings or with no compression.
package imagex

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"strings"

	"github.com/gen2brain/jpegli"
	xdraw "golang.org/x/image/draw"

	// Register the decoders: sites serve these formats too.
	_ "golang.org/x/image/webp"
	_ "image/gif"
)

// Result is what became of a source file.
type Result struct {
	Path      string // file to put into the book
	Name      string // file name; the extension may have changed (png to jpg)
	MediaType string
	Size      int64
	// Changed reports whether the result differs from the source.
	Changed bool
}

// Optimizer turns a source file into the one that goes into the book.
// Returning the source unchanged is a valid answer, not an error.
type Optimizer interface {
	Optimize(srcPath string) (Result, error)
}

// Passthrough hands files through untouched, for when compression is not wanted.
type Passthrough struct{}

// Optimize implements Optimizer.
func (Passthrough) Optimize(src string) (Result, error) {
	info, err := os.Stat(src)
	if err != nil {
		return Result{}, err
	}
	return Result{Path: src, Name: filepath.Base(src), MediaType: MediaType(ext(src)), Size: info.Size()}, nil
}

// Resizer scales pictures down and re-encodes them as JPEG.
//
// A picture with transparency stays PNG: as JPEG its background would turn black.
// Animations (gif) are left alone, since only the first frame would survive.
type Resizer struct {
	// MaxSize caps the longer side in pixels. 0 means no scaling.
	MaxSize int
	// Quality is the JPEG quality, 1..100. 0 means 82.
	Quality int
	// Dir is where results are written.
	Dir string
	// Scaler controls scaling quality. CatmullRom by default: it is noticeably
	// crisper than bilinear on maps and illustrations that contain text.
	Scaler xdraw.Interpolator
}

// NewResizer prepares an optimizer and creates the output directory.
func NewResizer(dir string, maxSize, quality int) (*Resizer, error) {
	if quality <= 0 || quality > 100 {
		quality = 82
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Resizer{MaxSize: maxSize, Quality: quality, Dir: dir, Scaler: xdraw.CatmullRom}, nil
}

// Optimize compresses a single file. When the compressed version comes out
// heavier than the original, the original is returned — small pictures do that.
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
		return original, nil // nothing to re-encode: the PNG would stay the same size
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

// fit scales a picture down to the cap on its longer side. It never scales up.
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
			return nil, fmt.Errorf("imagex: encoding png: %w", err)
		}
		return buf.Bytes(), nil
	}

	// Transparency is already ruled out by now, but the backing does no harm:
	// without it, semi-transparent pixels would come out black.
	var buf bytes.Buffer
	err := jpegli.Encode(&buf, flatten(img), &jpegli.EncodingOptions{
		Quality:              quality,
		ChromaSubsampling:    image.YCbCrSubsampleRatio420,
		OptimizeCoding:       true,
		AdaptiveQuantization: true,
	})
	if err != nil {
		return nil, fmt.Errorf("imagex: encoding jpeg: %w", err)
	}
	return buf.Bytes(), nil
}

// flatten composites a picture onto white when it has an alpha channel.
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

// hasAlpha reports whether any pixel is less than fully opaque. The format alone
// says nothing: PNGs are fully opaque more often than not.
func hasAlpha(img image.Image) bool {
	switch img.(type) {
	case *image.YCbCr, *image.Gray, *image.CMYK:
		return false // these formats have no alpha channel at all
	}
	b := img.Bounds()
	// Sample on a grid: walking every pixel of a large picture costs more than the re-encode.
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

// MediaType returns a MIME type from a file extension.
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
