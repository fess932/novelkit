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

// TestLive ходит на настоящий сайт: включается переменной RANOBELIB_LIVE=1.
// Качает две главы, поэтому запускать его в общем прогоне незачем.
func TestLive(t *testing.T) {
	if os.Getenv("RANOBELIB_LIVE") == "" {
		t.Skip("живой прогон выключен: RANOBELIB_LIVE=1 включит его")
	}
	const link = "https://ranobelib.me/ru/book/14841--beginning-after-the-end-novel"

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Источник подключается к реестру ровно так же, как подключался бы любой другой сайт.
	var registry novel.Registry
	registry.Register(ranobelib.NewSource(
		ranobelib.WithThrottle(900*time.Millisecond, 300*time.Millisecond),
		ranobelib.WithNotifier(func(n ranobelib.Notice) { t.Logf("клиент: %s (%v)", n.Message, n.Wait) }),
	))

	src, bookID, err := registry.Resolve(link)
	if err != nil {
		t.Fatalf("разбор ссылки: %v", err)
	}

	book, err := src.Book(ctx, bookID)
	if err != nil {
		t.Fatalf("карточка книги: %v", err)
	}
	t.Logf("книга: %s, аннотация %d символов", book.Title, len([]rune(book.Description)))
	if len(book.Editions) == 0 {
		t.Fatal("ни одного перевода")
	}
	for _, e := range book.Editions {
		t.Logf("перевод %q: %s — %d гл.", e.ID, e.Label(), e.Chapters)
	}

	store, err := job.OpenStore(filepath.Join(t.TempDir(), "jobs"))
	if err != nil {
		t.Fatal(err)
	}
	j, err := store.Plan(ctx, src, job.Request{
		BookID: bookID, EditionID: book.Editions[0].ID, From: 1, To: 2, WithImages: true,
	})
	if err != nil {
		t.Fatalf("планирование: %v", err)
	}
	if err := j.Download(ctx, src, job.DownloadOptions{
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
	// Сжатие включаем: заодно проверяется, что оно работает без внешних программ.
	opt, err := imagex.NewResizer(filepath.Join(j.Dir(), "min"), 1200, 82)
	if err != nil {
		t.Fatal(err)
	}
	res, err := j.BuildFile(ctx, src, out, job.BuildOptions{Optimizer: opt})
	if err != nil {
		t.Fatalf("сборка: %v", err)
	}
	t.Logf("собрано: %s — %d гл., %d илл., %.2f МБ (картинки %d КБ → %d КБ)",
		out, res.Chapters, res.Images, float64(res.Size)/1024/1024,
		res.ImagesBefore/1024, res.ImagesAfter/1024)

	if res.Chapters != 2 {
		t.Errorf("ожидалось 2 главы, получено %d", res.Chapters)
	}
	if st := j.State(); st.Book.Description == "" {
		t.Error("аннотация книги не заполнилась")
	}
}
