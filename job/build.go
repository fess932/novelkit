package job

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/fess932/novelkit/epub"
	"github.com/fess932/novelkit/imagex"
	"github.com/fess932/novelkit/novel"
)

// BuildOptions настраивают сборку книги.
type BuildOptions struct {
	// Optimizer пережимает иллюстрации. nil — класть оригиналы как есть.
	Optimizer imagex.Optimizer
	// CSS подменяет оформление книги.
	CSS string
	// OnWarning вызывается на некритичных бедах: пропущенная глава, битая картинка.
	OnWarning func(string)
}

// BuildResult — что получилось.
type BuildResult struct {
	Size     int64 // размер книги в байтах
	Chapters int   // сколько глав попало в книгу
	Images   int   // сколько картинок попало в книгу
	Missing  int   // сколько глав пропущено: их нет в кэше
	// ImagesBefore и ImagesAfter — суммарный вес картинок до и после сжатия.
	ImagesBefore, ImagesAfter int64
}

// BuildFile собирает книгу в файл.
func (j *Job) BuildFile(ctx context.Context, src novel.Source, path string, opts BuildOptions) (BuildResult, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return BuildResult{}, err
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return BuildResult{}, err
	}
	res, err := j.Build(ctx, src, f, opts)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return res, err
}

// Build собирает книгу из того, что лежит в кэше задания. В сеть не ходит.
//
// Источник нужен, чтобы разобрать сохранённые ответы: сырой вид понимает
// только тот сайт, который его отдал.
//
// Главы, которых в кэше нет, пропускаются и считаются в BuildResult.Missing —
// так недокачанную книгу всё равно можно собрать и почитать.
func (j *Job) Build(ctx context.Context, src novel.Source, w io.Writer, opts BuildOptions) (BuildResult, error) {
	st := j.State()
	if st.Source.ID != src.ID() {
		return BuildResult{}, fmt.Errorf("job: задание сделано источником %q, а передан %q", st.Source.ID, src.ID())
	}

	optimizer := opts.Optimizer
	if optimizer == nil {
		optimizer = imagex.Passthrough{}
	}
	warn := func(format string, args ...any) {
		if opts.OnWarning != nil {
			opts.OnWarning(fmt.Sprintf(format, args...))
		}
	}

	var res BuildResult
	// Картинки собираются по мере встречи в тексте: имя внутри книги может
	// отличаться от имени в кэше, если сжатие сменило формат.
	packed := map[string]epub.Image{}
	inBook := map[string]string{} // файл в кэше -> имя внутри книги

	resolver := novel.ResolverFunc(func(img novel.Image) (string, bool) {
		asset, ok := st.Assets[img.URL]
		if !ok {
			return "", false
		}
		if name, done := inBook[asset.File]; done {
			return "../images/" + name, true
		}

		src := j.assetPath(asset.File)
		info, err := os.Stat(src)
		if err != nil {
			return "", false // картинка не скачалась — из разметки её убираем
		}
		res.ImagesBefore += info.Size()

		out, err := optimizer.Optimize(src)
		if err != nil {
			warn("картинку не удалось пережать (%s): %v", asset.File, err)
			out = imagex.Result{Path: src, Name: asset.File, MediaType: imagex.MediaType(asset.Ext), Size: info.Size()}
		}
		data, err := os.ReadFile(out.Path)
		if err != nil {
			return "", false
		}
		res.ImagesAfter += int64(len(data))

		packed[out.Name] = epub.Image{Name: out.Name, MediaType: out.MediaType, Data: data}
		inBook[asset.File] = out.Name
		return "../images/" + out.Name, true
	})

	book := &epub.Book{Metadata: st.Book, CSS: opts.CSS}

	for _, cs := range st.Chapters {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		chapter, err := j.LoadChapter(src, cs.Index)
		if err != nil {
			res.Missing++
			continue
		}
		body := ""
		if chapter.Content != nil {
			body = chapter.Content.XHTML(resolver)
		}
		if trimSpace(body) == "" {
			body = `<p class="empty"> </p>`
		}
		book.Chapters = append(book.Chapters, epub.Chapter{
			Volume: cs.Volume,
			Number: cs.Number,
			Title:  cs.Title(),
			Body:   body,
		})
	}

	if len(book.Chapters) == 0 {
		return res, fmt.Errorf("job: нет ни одной скачанной главы — собирать нечего")
	}
	if res.Missing > 0 {
		warn("в книгу не попало глав: %d (нет в кэше)", res.Missing)
	}

	for _, img := range packed {
		book.Images = append(book.Images, img)
	}
	if st.Cover != nil {
		if cover, err := j.buildCover(*st.Cover, optimizer); err == nil {
			book.Cover = cover
		} else {
			warn("обложку не удалось приложить: %v", err)
		}
	}

	n, err := book.WriteTo(w)
	if err != nil {
		return res, err
	}
	res.Size = n
	res.Chapters = len(book.Chapters)
	res.Images = len(book.Images)
	return res, nil
}

func (j *Job) buildCover(asset Asset, optimizer imagex.Optimizer) (*epub.Image, error) {
	src := j.assetPath(asset.File)
	out, err := optimizer.Optimize(src)
	if err != nil {
		out = imagex.Result{Path: src, Name: asset.File, MediaType: imagex.MediaType(asset.Ext)}
	}
	data, err := os.ReadFile(out.Path)
	if err != nil {
		return nil, err
	}
	name := out.Name
	if !strings.HasPrefix(name, "cover.") {
		name = "cover" + filepath.Ext(name)
	}
	return &epub.Image{Name: name, MediaType: out.MediaType, Data: data}, nil
}
