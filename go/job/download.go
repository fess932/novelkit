package job

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/fess932/ranobelib/novel"
)

// DownloadOptions настраивают ход загрузки.
type DownloadOptions struct {
	// OnChapter вызывается после каждой успешно скачанной главы.
	OnChapter func(Event)
	// OnWarning вызывается на некритичных бедах — например, не скачалась картинка.
	// Такие беды не останавливают загрузку, но записываются в состояние задания.
	OnWarning func(string)
}

// Event — состояние загрузки после очередной главы.
type Event struct {
	Chapter  ChapterState
	Progress Progress
	// Elapsed — сколько времени идёт текущий запуск.
	Elapsed time.Duration
	// ETA — оценка оставшегося времени по средней скорости текущего запуска.
	ETA time.Duration
}

// ChapterError сообщает, на какой главе всё остановилось.
type ChapterError struct {
	Chapter ChapterState
	Err     error
}

func (e *ChapterError) Error() string {
	return fmt.Sprintf("job: глава %s (том %s): %v", e.Chapter.Number, e.Chapter.Volume, e.Err)
}

func (e *ChapterError) Unwrap() error { return e.Err }

// Download докачивает недостающие главы.
//
// Останавливается на первой неустранимой ошибке и возвращает *ChapterError:
// всё, что успело скачаться, уже лежит в кэше, повторный вызов продолжит с того же места.
// Отмена контекста тоже останавливает загрузку без потери скачанного.
func (j *Job) Download(ctx context.Context, src novel.Source, opts DownloadOptions) error {
	started := time.Now()
	var fetched int

	// Снимок плана: дальше состояние меняется только под мьютексом,
	// поэтому параллельный вызов State() не увидит его на полпути.
	j.mu.Lock()
	chapters := append([]ChapterState(nil), j.state.Chapters...)
	ref := j.state.Source
	withImages := j.state.WithImages
	j.mu.Unlock()

	if ref.ID != src.ID() {
		return fmt.Errorf("job: задание сделано источником %q, а передан %q", ref.ID, src.ID())
	}

	for i, ch := range chapters {
		if ch.Done && j.hasChapter(ch.Index) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		chapter, err := src.Chapter(ctx, ref.BookID, ref.EditionID, ch.Info())
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return &ChapterError{Chapter: ch, Err: err}
		}

		if err := writeFileAtomic(j.chapterPath(ch.Index), chapter.Raw); err != nil {
			return &ChapterError{Chapter: ch, Err: err}
		}

		if withImages {
			if err := j.fetchImages(ctx, src, chapter, opts); err != nil {
				return &ChapterError{Chapter: ch, Err: err}
			}
		}

		j.mu.Lock()
		if i < len(j.state.Chapters) && j.state.Chapters[i].Index == ch.Index {
			j.state.Chapters[i].Done = true
		}
		err = j.save()
		progress := j.state.progress()
		j.mu.Unlock()
		if err != nil {
			return &ChapterError{Chapter: ch, Err: err}
		}
		ch.Done = true

		fetched++
		if opts.OnChapter != nil {
			elapsed := time.Since(started)
			var eta time.Duration
			if left := progress.Left(); left > 0 {
				eta = time.Duration(int64(elapsed) / int64(fetched) * int64(left))
			}
			opts.OnChapter(Event{Chapter: ch, Progress: progress, Elapsed: elapsed, ETA: eta})
		}
	}
	return nil
}

// fetchImages находит картинки главы, разобрав её разметку, и качает недостающие.
// Битая картинка не роняет загрузку текста: она пропускается с предупреждением.
func (j *Job) fetchImages(ctx context.Context, src novel.Source, chapter *novel.Chapter, opts DownloadOptions) error {
	if chapter.Content == nil {
		return nil
	}

	var found []novel.Image
	chapter.Content.XHTML(novel.ResolverFunc(func(img novel.Image) (string, bool) {
		found = append(found, img)
		return "", false // на этом шаге разметка не нужна, нужны только адреса
	}))

	for _, img := range found {
		if img.URL == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		j.mu.Lock()
		asset, known := j.state.Assets[img.URL]
		j.mu.Unlock()
		if known {
			if _, err := os.Stat(j.assetPath(asset.File)); err == nil {
				continue
			}
		}

		ext := img.Ext
		if ext == "" {
			ext = extOf(img.URL)
		}
		data, _, err := src.Fetch(ctx, img.URL)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			msg := fmt.Sprintf("картинка не скачалась (%s): %v", chapter.Info.Title(), err)
			j.warn(msg)
			if opts.OnWarning != nil {
				opts.OnWarning(msg)
			}
			continue
		}

		name := assetName(img.URL, ext)
		if err := writeFileAtomic(j.assetPath(name), data); err != nil {
			return err
		}
		j.mu.Lock()
		j.state.Assets[img.URL] = Asset{File: name, Ext: ext}
		err = j.save()
		j.mu.Unlock()
		if err != nil {
			return err
		}
	}
	return nil
}

func (j *Job) hasChapter(index int) bool {
	_, err := os.Stat(j.chapterPath(index))
	return err == nil
}

// LoadChapter читает главу из кэша задания и разбирает её тем же источником.
func (j *Job) LoadChapter(src novel.Source, index int) (*novel.Chapter, error) {
	raw, err := os.ReadFile(j.chapterPath(index))
	if err != nil {
		return nil, err
	}
	ch, err := src.DecodeChapter(raw)
	if err != nil {
		return nil, fmt.Errorf("job: глава %d: %w", index, err)
	}
	return ch, nil
}
