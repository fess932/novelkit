package job_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/fess932/novelkit/job"
	"github.com/fess932/novelkit/markup"
	"github.com/fess932/novelkit/novel"
)

// fakeSource — источник целиком в памяти. Он же проверяет, что интерфейс
// novel.Source достаточен для нового сайта: ни одной ссылки на ranobelib здесь нет.
type fakeSource struct {
	// failFrom > 0 — начиная с этой главы отдавать «не найдено».
	failFrom atomic.Int64
	fetches  atomic.Int64
	chapters atomic.Int64
}

type fakeChapter struct {
	Index  int    `json:"index"`
	Volume string `json:"volume"`
	Number string `json:"number"`
	Name   string `json:"name"`
	HTML   string `json:"html"`
}

func (s *fakeSource) ID() string                       { return "fake" }
func (s *fakeSource) Supports(u string) bool           { return strings.Contains(u, "fake.test") }
func (s *fakeSource) ParseRef(u string) (string, bool) { return "book-1", s.Supports(u) }
func (s *fakeSource) Search(context.Context, string) ([]novel.Book, error) {
	return nil, novel.ErrUnsupported
}

func (s *fakeSource) Book(_ context.Context, id string) (*novel.Book, error) {
	if id != "book-1" {
		return nil, novel.ErrNotFound
	}
	return &novel.Book{
		ID:            id,
		Title:         "Тестовая книга",
		OriginalTitle: "Test Book",
		Authors:       []string{"Автор"},
		Genres:        []string{"Фэнтези"},
		Year:          "2015",
		Description:   "Аннотация книги.",
		CoverURL:      "https://fake.test/cover.jpg",
		URL:           "https://fake.test/book-1",
		Editions: []novel.Edition{
			{ID: "main", Name: "Основная", Teams: []string{"Команда", "Вторая команда"}, Chapters: 3},
			{ID: "empty", Name: "Заброшенный перевод"},
		},
	}, nil
}

func (s *fakeSource) Chapters(_ context.Context, bookID, editionID string) ([]novel.ChapterInfo, error) {
	if editionID != "main" {
		return nil, nil
	}
	return []novel.ChapterInfo{
		{ID: "1", Index: 1, Volume: "1", Number: "1", Name: "Первая"},
		{ID: "2", Index: 2, Volume: "1", Number: "2", Name: "Вторая"},
		{ID: "3", Index: 3, Volume: "2", Number: "3", Name: "Третья"},
	}, nil
}

func (s *fakeSource) Chapter(_ context.Context, bookID, editionID string, ci novel.ChapterInfo) (*novel.Chapter, error) {
	s.chapters.Add(1)
	if fail := s.failFrom.Load(); fail > 0 && int64(ci.Index) >= fail {
		return nil, fmt.Errorf("глава %s: %w", ci.Number, novel.ErrNotFound)
	}
	raw, err := json.Marshal(fakeChapter{
		Index: ci.Index, Volume: ci.Volume, Number: ci.Number, Name: ci.Name,
		HTML: fmt.Sprintf(`<p>Текст главы %s.</p><img src="https://fake.test/pic.jpg"/>`, ci.Number),
	})
	if err != nil {
		return nil, err
	}
	return s.DecodeChapter(raw)
}

func (s *fakeSource) DecodeChapter(raw []byte) (*novel.Chapter, error) {
	var fc fakeChapter
	if err := json.Unmarshal(raw, &fc); err != nil {
		return nil, err
	}
	return &novel.Chapter{
		Info:    novel.ChapterInfo{Index: fc.Index, Volume: fc.Volume, Number: fc.Number, Name: fc.Name},
		Content: markup.HTML(fc.HTML),
		Raw:     raw,
	}, nil
}

func (s *fakeSource) Fetch(_ context.Context, url string) ([]byte, string, error) {
	s.fetches.Add(1)
	return bytes.Repeat([]byte{0xff}, 64), "image/jpeg", nil
}

func setup(t *testing.T) (*fakeSource, *job.Store) {
	t.Helper()
	store, err := job.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &fakeSource{}, store
}

func plan(t *testing.T, store *job.Store, src novel.Source) *job.Job {
	t.Helper()
	j, err := store.Plan(context.Background(), src, job.Request{
		BookID: "book-1", EditionID: "main", WithImages: true,
	})
	if err != nil {
		t.Fatalf("планирование задания: %v", err)
	}
	return j
}

func TestPlanCollectsMetadata(t *testing.T) {
	src, store := setup(t)
	st := plan(t, store, src).State()

	if st.Book.Title != "Тестовая книга" || st.Book.Description != "Аннотация книги." {
		t.Errorf("метаданные книги: %+v", st.Book)
	}
	if st.Source.EditionLabel != "Команда & Вторая команда" {
		t.Errorf("подпись перевода: %q", st.Source.EditionLabel)
	}
	if len(st.Book.Translators) != 2 {
		t.Errorf("переводчики: %+v", st.Book.Translators)
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
	src, store := setup(t)
	j := plan(t, store, src)

	src.failFrom.Store(3) // третья глава недоступна
	err := j.Download(context.Background(), src, job.DownloadOptions{})

	var chErr *job.ChapterError
	if !errors.As(err, &chErr) {
		t.Fatalf("ожидалась ошибка главы, получено %v", err)
	}
	if chErr.Chapter.Number != "3" {
		t.Errorf("остановились не на той главе: %+v", chErr.Chapter)
	}
	if !errors.Is(err, novel.ErrNotFound) {
		t.Errorf("причина ошибки должна доставаться через errors.Is: %v", err)
	}
	if p := j.Progress(); p.Done != 2 || p.Left() != 1 {
		t.Fatalf("скачанное потеряно: %+v", p)
	}

	// Продолжение: качается только недостающая глава.
	src.failFrom.Store(0)
	before := src.chapters.Load()
	if err := j.Download(context.Background(), src, job.DownloadOptions{}); err != nil {
		t.Fatalf("продолжение загрузки: %v", err)
	}
	if p := j.Progress(); p.Done != 3 {
		t.Fatalf("после продолжения скачано %d из %d", p.Done, p.Total)
	}
	if got := src.chapters.Load() - before; got != 1 {
		t.Errorf("при продолжении запрошено глав: %d, а недоставало одной", got)
	}
}

// Одна и та же картинка качается один раз на всё задание.
func TestImagesFetchedOnce(t *testing.T) {
	src, store := setup(t)
	j := plan(t, store, src)
	before := src.fetches.Load() // обложка уже скачана в Plan

	if err := j.Download(context.Background(), src, job.DownloadOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := src.fetches.Load() - before; got != 1 {
		t.Errorf("картинку скачали %d раз(а), хотя во всех главах она одна", got)
	}
}

// Состояние переживает перезапуск: задание читается с диска как есть.
func TestReopenKeepsProgress(t *testing.T) {
	src, store := setup(t)
	j := plan(t, store, src)
	if err := j.Download(context.Background(), src, job.DownloadOptions{}); err != nil {
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
	src, store := setup(t)
	j := plan(t, store, src)
	if err := j.Download(context.Background(), src, job.DownloadOptions{}); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(t.TempDir(), "book.epub")
	res, err := j.BuildFile(context.Background(), src, out, job.BuildOptions{})
	if err != nil {
		t.Fatalf("сборка книги: %v", err)
	}
	if res.Chapters != 3 || res.Missing != 0 {
		t.Errorf("в книгу попало %d глав, пропущено %d", res.Chapters, res.Missing)
	}
	if res.Images != 1 {
		t.Errorf("картинок в книге: %d (одна и та же не должна дублироваться)", res.Images)
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
	src, store := setup(t)
	j := plan(t, store, src)
	src.failFrom.Store(2)
	_ = j.Download(context.Background(), src, job.DownloadOptions{})

	var warnings []string
	res, err := j.Build(context.Background(), src, &bytes.Buffer{}, job.BuildOptions{
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
	src, store := setup(t)
	j, err := store.Plan(context.Background(), src, job.Request{BookID: "book-1", EditionID: "main", From: 2})
	if err != nil {
		t.Fatal(err)
	}
	st := j.State()
	if len(st.Chapters) != 2 || st.Chapters[0].Number != "2" {
		t.Errorf("диапазон «со второй до конца» разобран неверно: %+v", st.Chapters)
	}
}

func TestPlanRejectsEmptyEdition(t *testing.T) {
	src, store := setup(t)
	if _, err := store.Plan(context.Background(), src, job.Request{BookID: "book-1", EditionID: "empty"}); err == nil {
		t.Fatal("ожидалась ошибка: в переводе нет глав")
	}
}

// Задание помнит свой источник и не даст скачивать себя чужим.
func TestJobRejectsForeignSource(t *testing.T) {
	src, store := setup(t)
	j := plan(t, store, src)

	other := &renamedSource{fakeSource: src}
	if err := j.Download(context.Background(), other, job.DownloadOptions{}); err == nil {
		t.Fatal("чужой источник должен отвергаться")
	}
	if _, err := j.Build(context.Background(), other, &bytes.Buffer{}, job.BuildOptions{}); err == nil {
		t.Fatal("сборка чужим источником должна отвергаться")
	}
}

type renamedSource struct{ *fakeSource }

func (r *renamedSource) ID() string { return "other" }
