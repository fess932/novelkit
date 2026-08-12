package job_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/fess932/novelkit/job"
	"github.com/fess932/novelkit/markup"
	"github.com/fess932/novelkit/novel"
)

// fakeSource is a source that lives entirely in memory. It doubles as proof
// that novel.Source is enough for a new site: nothing here mentions ranobelib.
type fakeSource struct {
	// failFrom > 0 makes every chapter from that index on report "not found".
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
		Title:         "Test Book",
		OriginalTitle: "Test Book (original)",
		Authors:       []string{"Author"},
		Genres:        []string{"Fantasy"},
		Year:          "2015",
		Description:   "The book blurb.",
		Language:      "ru",
		CoverURL:      "https://fake.test/cover.jpg",
		URL:           "https://fake.test/book-1",
		Editions: []novel.Edition{
			{ID: "main", Name: "Main", Teams: []string{"Team", "Second Team"}, Chapters: 3},
			{ID: "empty", Name: "Abandoned translation"},
		},
	}, nil
}

func (s *fakeSource) Chapters(_ context.Context, bookID, editionID string) ([]novel.ChapterInfo, error) {
	if editionID != "main" {
		return nil, nil
	}
	return []novel.ChapterInfo{
		{ID: "1", Index: 1, Volume: "1", Number: "1", Name: "First"},
		{ID: "2", Index: 2, Volume: "1", Number: "2", Name: "Second"},
		{ID: "3", Index: 3, Volume: "2", Number: "3", Name: "Third"},
	}, nil
}

func (s *fakeSource) Chapter(_ context.Context, bookID, editionID string, ci novel.ChapterInfo) (*novel.Chapter, error) {
	s.chapters.Add(1)
	if fail := s.failFrom.Load(); fail > 0 && int64(ci.Index) >= fail {
		return nil, fmt.Errorf("chapter %s: %w", ci.Number, novel.ErrNotFound)
	}
	raw, err := json.Marshal(fakeChapter{
		Index: ci.Index, Volume: ci.Volume, Number: ci.Number, Name: ci.Name,
		HTML: fmt.Sprintf(`<p>Text of chapter %s.</p><img src="https://fake.test/pic.jpg"/>`, ci.Number),
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
		t.Fatalf("planning the job: %v", err)
	}
	return j
}

func TestPlanCollectsMetadata(t *testing.T) {
	src, store := setup(t)
	st := plan(t, store, src).State()

	if st.Book.Title != "Test Book" || st.Book.Description != "The book blurb." {
		t.Errorf("book metadata: %+v", st.Book)
	}
	if st.Source.EditionLabel != "Team & Second Team" {
		t.Errorf("translation label: %q", st.Source.EditionLabel)
	}
	if len(st.Book.Translators) != 2 {
		t.Errorf("translators: %+v", st.Book.Translators)
	}
	if len(st.Chapters) != 3 {
		t.Fatalf("expected 3 chapters, got %d", len(st.Chapters))
	}
	if st.Cover == nil {
		t.Error("the cover was not downloaded")
	}
}

// A download stops at the first unrecoverable error, keeping what it already has.
func TestDownloadStopsAndResumes(t *testing.T) {
	src, store := setup(t)
	j := plan(t, store, src)

	src.failFrom.Store(3) // the third chapter is unavailable
	err := j.Download(context.Background(), src, job.DownloadOptions{})

	var chErr *job.ChapterError
	if !errors.As(err, &chErr) {
		t.Fatalf("expected a chapter error, got %v", err)
	}
	if chErr.Chapter.Number != "3" {
		t.Errorf("stopped at the wrong chapter: %+v", chErr.Chapter)
	}
	if !errors.Is(err, novel.ErrNotFound) {
		t.Errorf("the cause must be reachable through errors.Is: %v", err)
	}
	if p := j.Progress(); p.Done != 2 || p.Left() != 1 {
		t.Fatalf("downloaded chapters were lost: %+v", p)
	}

	// Resuming: only the missing chapter is fetched.
	src.failFrom.Store(0)
	before := src.chapters.Load()
	if err := j.Download(context.Background(), src, job.DownloadOptions{}); err != nil {
		t.Fatalf("resuming the download: %v", err)
	}
	if p := j.Progress(); p.Done != 3 {
		t.Fatalf("after resuming, %d of %d are downloaded", p.Done, p.Total)
	}
	if got := src.chapters.Load() - before; got != 1 {
		t.Errorf("resuming requested %d chapters, while only one was missing", got)
	}
}

// The same picture is downloaded once per job, no matter how often it appears.
func TestImagesFetchedOnce(t *testing.T) {
	src, store := setup(t)
	j := plan(t, store, src)
	before := src.fetches.Load() // Plan has already fetched the cover

	if err := j.Download(context.Background(), src, job.DownloadOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := src.fetches.Load() - before; got != 1 {
		t.Errorf("the picture was downloaded %d times, though every chapter shares one", got)
	}
}

// The state survives a restart: the job is read back from disk as it was.
func TestReopenKeepsProgress(t *testing.T) {
	src, store := setup(t)
	j := plan(t, store, src)
	if err := j.Download(context.Background(), src, job.DownloadOptions{}); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.Open(j.Dir())
	if err != nil {
		t.Fatalf("the job does not reopen: %v", err)
	}
	if p := reopened.Progress(); p.Done != 3 || p.Total != 3 {
		t.Errorf("progress after reopening: %+v", p)
	}

	jobs, err := store.List()
	if err != nil || len(jobs) != 1 {
		t.Fatalf("job list: %v, %d entries", err, len(jobs))
	}
}

// The language reported by the source reaches the book: its metadata and its
// chapter headings.
func TestBookLanguageComesFromSource(t *testing.T) {
	src, store := setup(t)
	j := plan(t, store, src)
	if err := j.Download(context.Background(), src, job.DownloadOptions{}); err != nil {
		t.Fatal(err)
	}
	if lang := j.State().Book.Language; lang != "ru" {
		t.Fatalf("book language: %q", lang)
	}

	var buf bytes.Buffer
	if _, err := j.Build(context.Background(), src, &buf, job.BuildOptions{}); err != nil {
		t.Fatal(err)
	}
	r, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	f, err := r.Open("OEBPS/text/ch0001.xhtml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	data, _ := io.ReadAll(f)
	if !strings.Contains(string(data), "Глава 1. First") {
		t.Errorf("the chapter heading did not follow the language:\n%s", data)
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
		t.Fatalf("assembling the book: %v", err)
	}
	if res.Chapters != 3 || res.Missing != 0 {
		t.Errorf("%d chapters made it into the book, %d were skipped", res.Chapters, res.Missing)
	}
	if res.Images != 1 {
		t.Errorf("pictures in the book: %d (the same one must not be duplicated)", res.Images)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("the book does not open: %v", err)
	}
	var names []string
	for _, f := range r.File {
		names = append(names, f.Name)
	}
	joined := strings.Join(names, "\n")
	for _, want := range []string{"mimetype", "OEBPS/content.opf", "OEBPS/nav.xhtml", "OEBPS/text/ch0003.xhtml", "OEBPS/images/cover"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the book is missing %s:\n%s", want, joined)
		}
	}
}

// A half-downloaded book can still be assembled: the missing chapters are left out.
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
		t.Fatalf("assembling a half-downloaded book: %v", err)
	}
	if res.Chapters != 1 || res.Missing != 2 {
		t.Errorf("%d chapters assembled, %d skipped", res.Chapters, res.Missing)
	}
	if len(warnings) == 0 {
		t.Error("no warning about the skipped chapters")
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
		t.Errorf("the range \"from the second to the end\" parsed wrong: %+v", st.Chapters)
	}
}

// Importing without choosing a translation and then importing the very one that
// was chosen must reuse the same cache instead of downloading it all again.
func TestUnspecifiedEditionReusesTheSameJob(t *testing.T) {
	src, store := setup(t)
	ctx := context.Background()

	auto, err := store.Plan(ctx, src, job.Request{BookID: "book-1"})
	if err != nil {
		t.Fatalf("planning without a translation: %v", err)
	}
	if err := auto.Download(ctx, src, job.DownloadOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := auto.State().Source.EditionID; got != "main" {
		t.Fatalf("an unspecified translation should settle on the fullest one, got %q", got)
	}

	before := src.chapters.Load()
	explicit, err := store.Plan(ctx, src, job.Request{BookID: "book-1", EditionID: "main"})
	if err != nil {
		t.Fatalf("planning with the same translation: %v", err)
	}
	if explicit.Dir() != auto.Dir() {
		t.Errorf("two cache directories for one translation:\n%s\n%s", auto.Dir(), explicit.Dir())
	}
	if err := explicit.Download(ctx, src, job.DownloadOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := src.chapters.Load() - before; got != 0 {
		t.Errorf("%d chapters were downloaded again", got)
	}
}

func TestPlanRejectsEmptyEdition(t *testing.T) {
	src, store := setup(t)
	if _, err := store.Plan(context.Background(), src, job.Request{BookID: "book-1", EditionID: "empty"}); err == nil {
		t.Fatal("expected an error: the translation has no chapters")
	}
}

// A job remembers its source and refuses to be driven by another one.
func TestJobRejectsForeignSource(t *testing.T) {
	src, store := setup(t)
	j := plan(t, store, src)

	other := &renamedSource{fakeSource: src}
	if err := j.Download(context.Background(), other, job.DownloadOptions{}); err == nil {
		t.Fatal("a foreign source must be rejected")
	}
	if _, err := j.Build(context.Background(), other, &bytes.Buffer{}, job.BuildOptions{}); err == nil {
		t.Fatal("assembling with a foreign source must be rejected")
	}
}

type renamedSource struct{ *fakeSource }

func (r *renamedSource) ID() string { return "other" }
