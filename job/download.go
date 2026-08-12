package job

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/fess932/novelkit/novel"
)

// DownloadOptions tune how a download proceeds.
type DownloadOptions struct {
	// OnChapter is called after every chapter that lands successfully.
	OnChapter func(Event)
	// OnWarning is called for non-fatal trouble, such as a picture that would not
	// download. It does not stop the run, but it is recorded in the job state.
	OnWarning func(string)
}

// Event is the state of the download after one more chapter.
type Event struct {
	Chapter  ChapterState
	Progress Progress
	// Elapsed is how long the current run has been going.
	Elapsed time.Duration
	// ETA estimates the time left from the average pace of the current run.
	ETA time.Duration
}

// ChapterError says which chapter brought the run to a halt.
type ChapterError struct {
	Chapter ChapterState
	Err     error
}

func (e *ChapterError) Error() string {
	return fmt.Sprintf("job: chapter %s (volume %s): %v", e.Chapter.Number, e.Chapter.Volume, e.Err)
}

func (e *ChapterError) Unwrap() error { return e.Err }

// Download fetches the chapters that are still missing.
//
// It stops at the first unrecoverable error and returns a *ChapterError:
// everything already fetched is in the cache, and calling it again resumes from
// the same place. Cancelling the context stops it just as safely.
func (j *Job) Download(ctx context.Context, src novel.Source, opts DownloadOptions) error {
	started := time.Now()
	var fetched int

	// Snapshot the plan: from here on the state changes only under the mutex,
	// so a concurrent State() never sees it half-updated.
	j.mu.Lock()
	chapters := append([]ChapterState(nil), j.state.Chapters...)
	ref := j.state.Source
	withImages := j.state.WithImages
	j.mu.Unlock()

	if ref.ID != src.ID() {
		return fmt.Errorf("job: the job belongs to source %q, but %q was passed", ref.ID, src.ID())
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

// fetchImages discovers a chapter's pictures by rendering its markup and
// downloads the missing ones. A broken picture does not sink the text: it is
// skipped with a warning.
func (j *Job) fetchImages(ctx context.Context, src novel.Source, chapter *novel.Chapter, opts DownloadOptions) error {
	if chapter.Content == nil {
		return nil
	}

	var found []novel.Image
	chapter.Content.XHTML(novel.ResolverFunc(func(img novel.Image) (string, bool) {
		found = append(found, img)
		return "", false // markup is not wanted here, only the addresses
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
			msg := fmt.Sprintf("picture download failed (%s): %v", chapter.Info.Title(), err)
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

// LoadChapter reads a chapter from the job cache and decodes it with the same source.
func (j *Job) LoadChapter(src novel.Source, index int) (*novel.Chapter, error) {
	raw, err := os.ReadFile(j.chapterPath(index))
	if err != nil {
		return nil, err
	}
	ch, err := src.DecodeChapter(raw)
	if err != nil {
		return nil, fmt.Errorf("job: chapter %d: %w", index, err)
	}
	return ch, nil
}
