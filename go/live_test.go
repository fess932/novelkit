package ranobelib_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fess932/ranobelib"
	"github.com/fess932/ranobelib/job"
)

// TestLive ходит на настоящий сайт: включается переменной RANOBELIB_LIVE=1.
// Качает две главы, поэтому запускать его в общем прогоне незачем.
func TestLive(t *testing.T) {
	if os.Getenv("RANOBELIB_LIVE") == "" {
		t.Skip("живой прогон выключен: RANOBELIB_LIVE=1 включит его")
	}
	const slug = "14841--beginning-after-the-end-novel"

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	c := ranobelib.New(
		ranobelib.WithThrottle(900*time.Millisecond, 300*time.Millisecond),
		ranobelib.WithNotifier(func(n ranobelib.Notice) { t.Logf("клиент: %s (%v)", n.Message, n.Wait) }),
	)

	manga, err := c.Manga(ctx, slug)
	if err != nil {
		t.Fatalf("карточка книги: %v", err)
	}
	t.Logf("книга: %s, аннотация %d символов", manga.Title(), len([]rune(manga.Summary.PlainText())))

	chapters, err := c.Chapters(ctx, slug)
	if err != nil {
		t.Fatalf("список глав: %v", err)
	}
	cards, err := c.Branches(ctx, manga.ID)
	if err != nil {
		t.Fatalf("ветки перевода: %v", err)
	}

	branches := ranobelib.CollectBranches(chapters, cards)
	if len(branches) == 0 {
		t.Fatal("ни одной ветки перевода")
	}
	for _, b := range branches {
		t.Logf("ветка %d: %s — %d гл.", b.ID, b.Label(), b.Count)
	}

	store, err := job.OpenStore(filepath.Join(t.TempDir(), "jobs"))
	if err != nil {
		t.Fatal(err)
	}
	j, err := store.Plan(ctx, c, job.Request{
		Slug: slug, BranchID: branches[0].ID, From: 1, To: 2, WithImages: true,
	})
	if err != nil {
		t.Fatalf("планирование: %v", err)
	}
	if err := j.Download(ctx, c, job.DownloadOptions{
		OnChapter: func(e job.Event) {
			t.Logf("%d/%d %s", e.Progress.Done, e.Progress.Total, e.Chapter.Title())
		},
		OnWarning: func(msg string) { t.Logf("предупреждение: %s", msg) },
	}); err != nil {
		t.Fatalf("загрузка: %v", err)
	}

	out := os.Getenv("RANOBELIB_LIVE_OUT")
	if out == "" {
		out = filepath.Join(t.TempDir(), "live.epub")
	}
	res, err := j.BuildFile(ctx, out, job.BuildOptions{})
	if err != nil {
		t.Fatalf("сборка: %v", err)
	}
	t.Logf("собрано: %s — %d гл., %d илл., %.2f МБ", out, res.Chapters, res.Images, float64(res.Size)/1024/1024)

	if res.Chapters != 2 {
		t.Errorf("ожидалось 2 главы, получено %d", res.Chapters)
	}
	if st := j.State(); st.Book.Description == "" {
		t.Error("аннотация книги не заполнилась")
	}
}
