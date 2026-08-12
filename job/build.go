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

// BuildOptions tune how a book is assembled.
type BuildOptions struct {
	// Optimizer re-compresses illustrations. nil puts the originals in as they are.
	Optimizer imagex.Optimizer
	// CSS replaces the book's styling.
	CSS string
	// OnWarning is called for non-fatal trouble: a skipped chapter, a broken picture.
	OnWarning func(string)
}

// BuildResult is what came out.
type BuildResult struct {
	Size     int64 // book size in bytes
	Chapters int   // chapters that made it into the book
	Images   int   // pictures that made it into the book
	Missing  int   // chapters skipped because the cache has none
	// ImagesBefore and ImagesAfter are the total picture weight before and after compression.
	ImagesBefore, ImagesAfter int64
}

// BuildFile assembles the book into a file.
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

// Build assembles the book from whatever the job cache holds. It never touches
// the network.
//
// The source is needed to decode the stored responses: only the site that
// produced them understands their raw form.
//
// Chapters missing from the cache are skipped and counted in BuildResult.Missing,
// so a half-downloaded book can still be assembled and read.
func (j *Job) Build(ctx context.Context, src novel.Source, w io.Writer, opts BuildOptions) (BuildResult, error) {
	st := j.State()
	if st.Source.ID != src.ID() {
		return BuildResult{}, fmt.Errorf("job: the job belongs to source %q, but %q was passed", st.Source.ID, src.ID())
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
	// Pictures are collected as the text mentions them: the name inside the book
	// may differ from the cached one when compression changed the format.
	packed := map[string]epub.Image{}
	inBook := map[string]string{} // cached file -> name inside the book

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
			return "", false // the picture never downloaded, so drop it from the markup
		}
		res.ImagesBefore += info.Size()

		out, err := optimizer.Optimize(src)
		if err != nil {
			warn("could not re-compress a picture (%s): %v", asset.File, err)
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
		return res, fmt.Errorf("job: no chapters downloaded, nothing to assemble")
	}
	if res.Missing > 0 {
		warn("chapters left out of the book: %d (not in the cache)", res.Missing)
	}

	for _, img := range packed {
		book.Images = append(book.Images, img)
	}
	if st.Cover != nil {
		if cover, err := j.buildCover(*st.Cover, optimizer); err == nil {
			book.Cover = cover
		} else {
			warn("could not attach the cover: %v", err)
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
