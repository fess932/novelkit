package ranobelib_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fess932/novelkit/imagex"
	"github.com/fess932/novelkit/job"
	"github.com/fess932/novelkit/novel"
	"github.com/fess932/novelkit/sources/ranobelib"
)

// TestLive talks to the real site; RANOBELIB_LIVE=1 turns it on. It downloads
// two chapters, so there is no point in running it with everything else.
func TestLive(t *testing.T) {
	if os.Getenv("RANOBELIB_LIVE") == "" {
		t.Skip("live run disabled; set RANOBELIB_LIVE=1 to enable it")
	}
	const link = "https://ranobelib.me/ru/book/14841--beginning-after-the-end-novel"

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// The source is registered exactly the way any other site would be.
	var registry novel.Registry
	registry.Register(ranobelib.NewSource(
		ranobelib.WithThrottle(900*time.Millisecond, 300*time.Millisecond),
		ranobelib.WithNotifier(func(n ranobelib.Notice) { t.Logf("client: %s (%v)", n.Message, n.Wait) }),
	))

	src, bookID, err := registry.Resolve(link)
	if err != nil {
		t.Fatalf("parsing the link: %v", err)
	}

	book, err := src.Book(ctx, bookID)
	if err != nil {
		t.Fatalf("book details: %v", err)
	}
	t.Logf("book: %s, blurb %d characters", book.Title, len([]rune(book.Description)))
	if len(book.Editions) == 0 {
		t.Fatal("no translations at all")
	}
	for _, e := range book.Editions {
		t.Logf("translation %q: %s — %d chapters", e.ID, e.Label(), e.Chapters)
	}

	store, err := job.OpenStore(filepath.Join(t.TempDir(), "jobs"))
	if err != nil {
		t.Fatal(err)
	}
	j, err := store.Plan(ctx, src, job.Request{
		BookID: bookID, EditionID: book.Editions[0].ID, From: 1, To: 2, WithImages: true,
	})
	if err != nil {
		t.Fatalf("planning: %v", err)
	}
	if err := j.Download(ctx, src, job.DownloadOptions{
		OnChapter: func(e job.Event) {
			t.Logf("%d/%d %s", e.Progress.Done, e.Progress.Total, e.Chapter.Title())
		},
		OnWarning: func(msg string) { t.Logf("warning: %s", msg) },
	}); err != nil {
		t.Fatalf("download: %v", err)
	}

	out := os.Getenv("RANOBELIB_LIVE_OUT")
	if out == "" {
		out = filepath.Join(t.TempDir(), "live.epub")
	}
	// Compression on: this also proves it works without any external program.
	opt, err := imagex.NewResizer(filepath.Join(j.Dir(), "min"), 1200, 82)
	if err != nil {
		t.Fatal(err)
	}
	res, err := j.BuildFile(ctx, src, out, job.BuildOptions{Optimizer: opt})
	if err != nil {
		t.Fatalf("assembly: %v", err)
	}
	t.Logf("assembled: %s — %d chapters, %d pictures, %.2f MB (images %d KB -> %d KB)",
		out, res.Chapters, res.Images, float64(res.Size)/1024/1024,
		res.ImagesBefore/1024, res.ImagesAfter/1024)

	if res.Chapters != 2 {
		t.Errorf("expected 2 chapters, got %d", res.Chapters)
	}
	if st := j.State(); st.Book.Description == "" {
		t.Error("the book blurb was never filled in")
	}
}
