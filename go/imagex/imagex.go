// Package imagex сжимает иллюстрации перед укладкой в книгу.
//
// Оригиналы никогда не меняются: результат пишется в отдельный каталог,
// поэтому книгу всегда можно пересобрать с другими настройками или без сжатия.
package imagex

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Result — что получилось из исходного файла.
type Result struct {
	Path      string // путь к файлу, который надо класть в книгу
	Name      string // имя файла (расширение могло смениться: png → jpg)
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

// ErrNoMagick возвращается, если ImageMagick не установлен.
var ErrNoMagick = errors.New("imagex: ImageMagick не найден (brew install imagemagick)")

// FindMagick ищет ImageMagick: сначала 7-й версии (magick), затем 6-й (convert).
func FindMagick() (bin string, im7 bool, err error) {
	if p, e := exec.LookPath("magick"); e == nil {
		return p, true, nil
	}
	if p, e := exec.LookPath("convert"); e == nil {
		return p, false, nil
	}
	return "", false, ErrNoMagick
}

// Magick сжимает картинки через ImageMagick.
type Magick struct {
	bin string
	im7 bool

	// MaxSize — предел по большей стороне в пикселях. 0 — не уменьшать.
	MaxSize int
	// Quality — качество jpeg, 1..100.
	Quality int
	// Dir — каталог для результатов.
	Dir string
}

// NewMagick готовит оптимизатор. Возвращает ErrNoMagick, если программы нет.
func NewMagick(dir string, maxSize, quality int) (*Magick, error) {
	bin, im7, err := FindMagick()
	if err != nil {
		return nil, err
	}
	if quality <= 0 || quality > 100 {
		quality = 82
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Magick{bin: bin, im7: im7, MaxSize: maxSize, Quality: quality, Dir: dir}, nil
}

type probe struct {
	width, height int
	alpha         bool
}

func (m *Magick) probe(src string) (probe, error) {
	args := []string{}
	name := m.bin
	if m.im7 {
		args = append(args, "identify")
	} else if p, err := exec.LookPath("identify"); err == nil {
		name = p
	}
	args = append(args, "-format", "%w %h %A", src+"[0]")

	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return probe{}, fmt.Errorf("imagex: не прочитать %s: %w", filepath.Base(src), err)
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 {
		return probe{}, fmt.Errorf("imagex: непонятный ответ identify для %s", filepath.Base(src))
	}
	w, _ := strconv.Atoi(fields[0])
	h, _ := strconv.Atoi(fields[1])
	alpha := len(fields) > 2 && (strings.EqualFold(fields[2], "true") || strings.EqualFold(fields[2], "blend"))
	return probe{width: w, height: h, alpha: alpha}, nil
}

// Optimize сжимает один файл. Анимацию и вектор не трогает, картинку с
// прозрачностью оставляет png (в jpeg у неё почернел бы фон), а если результат
// вышел тяжелее исходника — возвращает исходник.
func (m *Magick) Optimize(src string) (Result, error) {
	info, err := os.Stat(src)
	if err != nil {
		return Result{}, err
	}
	original := Result{
		Path: src, Name: filepath.Base(src),
		MediaType: mediaType(ext(src)), Size: info.Size(),
	}

	switch ext(src) {
	case "gif", "svg":
		return original, nil
	}

	p, err := m.probe(src)
	if err != nil {
		return original, err
	}

	needResize := m.MaxSize > 0 && max(p.width, p.height) > m.MaxSize
	target := "jpg"
	if p.alpha {
		target = "png"
		if !needResize {
			return original, nil // уменьшать нечего, а перекодировать незачем
		}
	}

	name := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src)) + "." + target
	dst := filepath.Join(m.Dir, name)

	if st, err := os.Stat(dst); err != nil || st.Size() == 0 {
		args := []string{src, "-auto-orient", "-strip"}
		if needResize {
			args = append(args, "-resize", fmt.Sprintf("%dx%d>", m.MaxSize, m.MaxSize))
		}
		if target == "jpg" {
			args = append(args, "-quality", strconv.Itoa(m.Quality),
				"-sampling-factor", "4:2:0", "-interlace", "Plane")
		} else {
			args = append(args, "-define", "png:compression-level=9")
		}
		args = append(args, dst)
		if out, err := exec.Command(m.bin, args...).CombinedOutput(); err != nil {
			return original, fmt.Errorf("imagex: %s: %w: %s", filepath.Base(src), err, strings.TrimSpace(string(out)))
		}
	}

	st, err := os.Stat(dst)
	if err != nil {
		return original, err
	}
	if st.Size() >= info.Size() {
		return original, nil
	}
	return Result{Path: dst, Name: name, MediaType: mediaType(target), Size: st.Size(), Changed: true}, nil
}

// Passthrough отдаёт файлы как есть — заглушка, когда сжатие не нужно.
type Passthrough struct{}

// Optimize реализует Optimizer.
func (Passthrough) Optimize(src string) (Result, error) {
	info, err := os.Stat(src)
	if err != nil {
		return Result{}, err
	}
	return Result{Path: src, Name: filepath.Base(src), MediaType: mediaType(ext(src)), Size: info.Size()}, nil
}

func ext(p string) string {
	return strings.ToLower(strings.TrimPrefix(filepath.Ext(p), "."))
}

func mediaType(e string) string {
	switch e {
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
