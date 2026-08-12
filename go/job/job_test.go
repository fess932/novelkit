package job_test

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/fess932/ranobelib"
	"github.com/fess932/ranobelib/job"
)

// fakeSite — минимальный сайт: карточка, список глав, ветки, главы и картинка.
type fakeSite struct {
	// failFrom > 0 — начиная с этого номера главы отдавать 404.
	failFrom atomic.Int64
	requests atomic.Int64
}

func (s *fakeSite) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/manga/1--test/chapters", func(w http.ResponseWriter, r *http.Request) {
		s.requests.Add(1)
		fmt.Fprint(w, `{"data":[
			{"id":1,"index":1,"volume":"1","number":"1","name":"Первая","branches":[{"branch_id":5,"teams":[{"name":"Команда"}],"user":{"username":"uploader"}}]},
			{"id":2,"index":2,"volume":"1","number":"2","name":"Вторая","branches":[{"branch_id":5,"teams":[{"name":"Команда"}],"user":{"username":"uploader"}}]},
			{"id":3,"index":3,"volume":"2","number":"3","name":"Третья","branches":[{"branch_id":5,"teams":[{"name":"Команда"}],"user":{"username":"uploader"}}]}
		]}`)
	})

	mux.HandleFunc("/manga/1--test", func(w http.ResponseWriter, r *http.Request) {
		s.requests.Add(1)
		fmt.Fprint(w, `{"data":{"id":1,"rus_name":"Тестовая книга","eng_name":"Test Book","slug_url":"1--test",
			"cover":{"default":"/uploads/cover.jpg"},
			"authors":[{"name":"Автор"}],"genres":[{"name":"Фэнтези"}],"releaseDate":"2015",
			"summary":{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"Аннотация книги."}]}]}}}`)
	})

	mux.HandleFunc("/branches/1", func(w http.ResponseWriter, r *http.Request) {
		s.requests.Add(1)
		fmt.Fprint(w, `{"data":[{"id":5,"name":"Основная","teams":[{"name":"Команда"},{"name":"Вторая команда"}]}]}`)
	})

	mux.HandleFunc("/manga/1--test/chapter", func(w http.ResponseWriter, r *http.Request) {
		s.requests.Add(1)
		number := r.URL.Query().Get("number")
		if fail := s.failFrom.Load(); fail > 0 && number >= fmt.Sprint(fail) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		fmt.Fprintf(w, `{"data":{"id":%s,"volume":"1","number":"%s","name":"Глава",
			"content":{"type":"doc","content":[
				{"type":"paragraph","content":[{"type":"text","text":"Текст главы %s."}]},
				{"type":"image","attrs":{"images":[{"image":"pic"}]}}
			]},
			"attachments":[{"name":"pic","extension":"jpg","url":"/uploads/pic.jpg"}]}}`, number, number, number)
	})

	mux.HandleFunc("/uploads/", func(w http.ResponseWriter, r *http.Request) {
		s.requests.Add(1)
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(bytes.Repeat([]byte{0xff}, 64))
	})

	return mux
}

func setup(t *testing.T) (*fakeSite, *ranobelib.Client, *job.Store) {
	t.Helper()
	site := &fakeSite{}
	srv := httptest.NewServer(site.handler())
	t.Cleanup(srv.Close)

	c := ranobelib.New(
		ranobelib.WithAPIURL(srv.URL),
		ranobelib.WithSiteURL(srv.URL),
		ranobelib.WithThrottle(0, 0),
		ranobelib.WithRetries(0),
	)
	store, err := job.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return site, c, store
}

func plan(t *testing.T, store *job.Store, c *ranobelib.Client) *job.Job {
	t.Helper()
	j, err := store.Plan(context.Background(), c, job.Request{
		Slug: "1--test", BranchID: 5, WithImages: true,
	})
	if err != nil {
		t.Fatalf("планирование задания: %v", err)
	}
	return j
}

func TestPlanCollectsMetadata(t *testing.T) {
	_, c, store := setup(t)
	j := plan(t, c2s(store), c)

	st := j.State()
	if st.Book.Title != "Тестовая книга" {
		t.Errorf("название книги: %q", st.Book.Title)
	}
	// Аннотация приходит документом ProseMirror: она должна стать текстом, а не «[object Object]».
	if st.Book.Description != "Аннотация книги." {
		t.Errorf("аннотация разобрана как %q", st.Book.Description)
	}
	if st.Source.BranchLabel != "Команда & Вторая команда" {
		t.Errorf("подпись ветки: %q", st.Source.BranchLabel)
	}
	if len(st.Chapters) != 3 {
		t.Fatalf("ожидалось 3 главы, получено %d", len(st.Chapters))
	}
	if st.Cover == nil {
		t.Error("обложка не скачалась")
	}
}

// Загрузка останавливается на первой неустранимой ошибке, а скачанное остаётся.
func TestDownloadStopsAndResumes(t *testing.T) {
	site, c, store := setup(t)
	j := plan(t, store, c)

	site.failFrom.Store(3) // третья глава недоступна
	err := j.Download(context.Background(), c, job.DownloadOptions{})

	var chErr *job.ChapterError
	if !errors.As(err, &chErr) {
		t.Fatalf("ожидалась ошибка главы, получено %v", err)
	}
	if chErr.Chapter.Number != "3" {
		t.Errorf("остановились не на той главе: %+v", chErr.Chapter)
	}
	if !errors.Is(err, ranobelib.ErrNotFound) {
		t.Errorf("причина ошибки должна доставаться через errors.Is: %v", err)
	}
	if p := j.Progress(); p.Done != 2 || p.Left() != 1 {
		t.Fatalf("скачанное потеряно: %+v", p)
	}

	// Продолжение: качается только недостающая глава.
	site.failFrom.Store(0)
	before := site.requests.Load()
	if err := j.Download(context.Background(), c, job.DownloadOptions{}); err != nil {
		t.Fatalf("продолжение загрузки: %v", err)
	}
	if p := j.Progress(); p.Done != 3 {
		t.Fatalf("после продолжения скачано %d из %d", p.Done, p.Total)
	}
	// Одна глава: сам запрос главы плюс, возможно, картинка — но не три главы заново.
	if got := site.requests.Load() - before; got > 2 {
		t.Errorf("при продолжении сделано %d запросов — перекачиваются уже скачанные главы", got)
	}
}

// Состояние переживает перезапуск: задание читается с диска как есть.
func TestReopenKeepsProgress(t *testing.T) {
	_, c, store := setup(t)
	j := plan(t, store, c)
	if err := j.Download(context.Background(), c, job.DownloadOptions{}); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.Open(j.Dir())
	if err != nil {
		t.Fatalf("задание не перечитывается: %v", err)
	}
	if p := reopened.Progress(); p.Done != 3 || p.Total != 3 {
		t.Errorf("прогресс после перечитывания: %+v", p)
	}

	jobs, err := store.List()
	if err != nil || len(jobs) != 1 {
		t.Fatalf("список заданий: %v, %d штук", err, len(jobs))
	}
}

func TestBuildProducesReadableBook(t *testing.T) {
	_, c, store := setup(t)
	j := plan(t, store, c)
	if err := j.Download(context.Background(), c, job.DownloadOptions{}); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(t.TempDir(), "book.epub")
	res, err := j.BuildFile(context.Background(), out, job.BuildOptions{})
	if err != nil {
		t.Fatalf("сборка книги: %v", err)
	}
	if res.Chapters != 3 || res.Missing != 0 {
		t.Errorf("в книгу попало %d глав, пропущено %d", res.Chapters, res.Missing)
	}
	if res.Images != 1 {
		t.Errorf("картинок в книге: %d (одна и та же картинка не должна дублироваться)", res.Images)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("книга не открывается: %v", err)
	}
	var names []string
	for _, f := range r.File {
		names = append(names, f.Name)
	}
	joined := strings.Join(names, "\n")
	for _, want := range []string{"mimetype", "OEBPS/content.opf", "OEBPS/nav.xhtml", "OEBPS/text/ch0003.xhtml", "OEBPS/images/cover"} {
		if !strings.Contains(joined, want) {
			t.Errorf("в книге нет %s:\n%s", want, joined)
		}
	}
}

// Недокачанную книгу тоже можно собрать: пропущенные главы просто не попадают внутрь.
func TestBuildSkipsMissingChapters(t *testing.T) {
	site, c, store := setup(t)
	j := plan(t, store, c)
	site.failFrom.Store(2)
	_ = j.Download(context.Background(), c, job.DownloadOptions{})

	var warnings []string
	res, err := j.Build(context.Background(), &bytes.Buffer{}, job.BuildOptions{
		OnWarning: func(msg string) { warnings = append(warnings, msg) },
	})
	if err != nil {
		t.Fatalf("сборка недокачанной книги: %v", err)
	}
	if res.Chapters != 1 || res.Missing != 2 {
		t.Errorf("собрано глав %d, пропущено %d", res.Chapters, res.Missing)
	}
	if len(warnings) == 0 {
		t.Error("о пропущенных главах не предупредили")
	}
}

func TestRangeSelection(t *testing.T) {
	_, c, store := setup(t)
	j, err := store.Plan(context.Background(), c, job.Request{Slug: "1--test", BranchID: 5, From: 2})
	if err != nil {
		t.Fatal(err)
	}
	st := j.State()
	if len(st.Chapters) != 2 || st.Chapters[0].Number != "2" {
		t.Errorf("диапазон «со второй до конца» разобран неверно: %+v", st.Chapters)
	}
}

func TestPlanRejectsEmptyBranch(t *testing.T) {
	_, c, store := setup(t)
	if _, err := store.Plan(context.Background(), c, job.Request{Slug: "1--test", BranchID: 999}); err == nil {
		t.Fatal("ожидалась ошибка: в ветке нет глав")
	}
}

// c2s нужен только чтобы не плодить обёртки в первом тесте.
func c2s(s *job.Store) *job.Store { return s }
