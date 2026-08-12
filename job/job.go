// Package job keeps downloads on disk and resumes them after an interruption.
//
// A job is a directory: job.json for the state, raw chapter responses under
// chapters/ and pictures under assets/. A chapter is marked done right after it
// is written, so an interruption never costs the chapters before it — the next
// run picks up exactly where it stopped.
//
// The package knows nothing about any site: it works with any novel.Source.
package job

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fess932/novelkit/epub"
	"github.com/fess932/novelkit/markup"
	"github.com/fess932/novelkit/novel"
)

// Version is the job format version. A directory written by another version is not read.
const Version = 1

// SourceRef says what is being downloaded and from where.
type SourceRef struct {
	// ID of the source, e.g. "ranobelib". It ties the job back to its site.
	ID     string `json:"id"`
	BookID string `json:"book_id"`
	// EditionID is the chosen translation; empty when the site has none.
	EditionID    string `json:"edition_id"`
	EditionLabel string `json:"edition_label"`
	BookURL      string `json:"book_url"`
}

// Asset is a downloaded picture.
type Asset struct {
	File string `json:"file"` // file name inside assets/
	Ext  string `json:"ext"`
}

// ChapterState is a chapter of the job plus whether it has been downloaded.
type ChapterState struct {
	ID     string `json:"id,omitempty"`
	Index  int    `json:"index"`
	Volume string `json:"volume"`
	Number string `json:"number"`
	Name   string `json:"name"`
	Done   bool   `json:"done"`
}

// Info returns the chapter in its common shape.
func (c ChapterState) Info() novel.ChapterInfo {
	return novel.ChapterInfo{ID: c.ID, Index: c.Index, Volume: c.Volume, Number: c.Number, Name: c.Name}
}

// Title builds the chapter heading.
func (c ChapterState) Title() string { return c.Info().Title() }

// State is the content of job.json.
type State struct {
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	Source     SourceRef        `json:"source"`
	WithImages bool             `json:"with_images"`
	Book       epub.Metadata    `json:"book"`
	Cover      *Asset           `json:"cover,omitempty"`
	Assets     map[string]Asset `json:"assets"` // keyed by picture address
	Chapters   []ChapterState   `json:"chapters"`
	Warnings   []string         `json:"warnings,omitempty"`
}

// Progress counts the chapters that are done.
type Progress struct {
	Done  int
	Total int
}

// Left returns how many chapters are still missing.
func (p Progress) Left() int { return p.Total - p.Done }

// Request describes what to download.
type Request struct {
	// BookID identifies the book within its source.
	BookID string
	// EditionID selects the translation. Empty means the only or unnamed one.
	EditionID string
	// From and To are chapter positions within the translation, starting at 1.
	// To <= 0 means "through the last chapter".
	From, To int
	// WithImages turns on downloading illustrations and the cover.
	WithImages bool
}

// Store is the directory jobs live in.
type Store struct {
	root string
}

// OpenStore opens the job directory, creating it when needed.
func OpenStore(root string) (*Store, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &Store{root: root}, nil
}

// Root returns the path to the job directory.
func (s *Store) Root() string { return s.root }

// Job is a single download job.
type Job struct {
	dir string

	mu    sync.Mutex
	state State
}

// Dir returns the job's directory.
func (j *Job) Dir() string { return j.dir }

// State returns a copy of the state; callers must not mutate it in place.
func (j *Job) State() State {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.state.clone()
}

// Progress reports how many chapters are already downloaded.
func (j *Job) Progress() Progress {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.state.progress()
}

func (s State) progress() Progress {
	p := Progress{Total: len(s.Chapters)}
	for _, c := range s.Chapters {
		if c.Done {
			p.Done++
		}
	}
	return p
}

func (s State) clone() State {
	out := s
	out.Chapters = append([]ChapterState(nil), s.Chapters...)
	out.Warnings = append([]string(nil), s.Warnings...)
	out.Assets = make(map[string]Asset, len(s.Assets))
	for k, v := range s.Assets {
		out.Assets[k] = v
	}
	return out
}

// List returns every job in the directory, most recent first.
func (s *Store) List() ([]*Job, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, err
	}
	jobs := make([]*Job, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		j, err := s.Open(filepath.Join(s.root, e.Name()))
		if err != nil {
			continue // skip foreign or half-written directories
		}
		jobs = append(jobs, j)
	}
	sort.SliceStable(jobs, func(i, k int) bool {
		return jobs[i].state.UpdatedAt.After(jobs[k].state.UpdatedAt)
	})
	return jobs, nil
}

// Open reads a job from a directory.
func (s *Store) Open(dir string) (*Job, error) {
	data, err := os.ReadFile(filepath.Join(dir, "job.json"))
	if err != nil {
		return nil, err
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("job: %s: %w", dir, err)
	}
	if st.Version != Version {
		return nil, fmt.Errorf("job: %s: format version %d, supported version is %d", dir, st.Version, Version)
	}
	if st.Assets == nil {
		st.Assets = map[string]Asset{}
	}
	return &Job{dir: dir, state: st}, nil
}

// Plan creates a job or extends an existing one.
//
// Chapters already downloaded are left alone: widening the range only adds the
// new ones. It goes to the network for the book details, the chapter list and
// the cover.
func (s *Store) Plan(ctx context.Context, src novel.Source, req Request) (*Job, error) {
	book, err := src.Book(ctx, req.BookID)
	if err != nil {
		return nil, err
	}
	// An unspecified translation is settled here, once, so that everything below
	// — the chapter list, the cache directory, the saved state — talks about the
	// same one.
	req.EditionID = resolveEdition(book, req.EditionID)

	chapters, err := src.Chapters(ctx, req.BookID, req.EditionID)
	if err != nil {
		return nil, err
	}
	if len(chapters) == 0 {
		return nil, fmt.Errorf("job: the selected translation has no chapters")
	}

	edition, ok := book.Edition(req.EditionID)
	if !ok {
		// A source may have no notion of translations at all, which is fine.
		edition = novel.Edition{ID: req.EditionID, Chapters: len(chapters)}
	}
	// The job directory is keyed by the translation, so the identifier has to be
	// settled before it is built: importing without choosing and then importing
	// the very translation that was chosen must land in the same directory
	// instead of downloading everything a second time.
	editionID := edition.ID

	novel.SortChapters(chapters)
	from, to := req.From, req.To
	if from < 1 {
		from = 1
	}
	if to <= 0 || to > len(chapters) {
		to = len(chapters)
	}
	if from > to {
		return nil, fmt.Errorf("job: empty chapter range %d-%d", from, to)
	}
	chapters = chapters[from-1 : to]

	dir := filepath.Join(s.root, dirName(src.ID(), req.BookID, editionID))
	for _, sub := range []string{"chapters", "assets"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, err
		}
	}

	j, err := s.Open(dir)
	if err != nil {
		j = &Job{dir: dir, state: State{
			Version:   Version,
			CreatedAt: time.Now(),
			Assets:    map[string]Asset{},
		}}
	}

	// Keep the state update in its own scope: fetching the cover below takes
	// the same mutex.
	if err := func() error {
		j.mu.Lock()
		defer j.mu.Unlock()

		// Keep what is downloaded, add the new chapters.
		done := make(map[int]ChapterState, len(j.state.Chapters))
		for _, ch := range j.state.Chapters {
			done[ch.Index] = ch
		}
		next := make([]ChapterState, 0, len(chapters))
		for _, ci := range chapters {
			if prev, ok := done[ci.Index]; ok {
				next = append(next, prev)
				continue
			}
			next = append(next, ChapterState{
				ID: ci.ID, Index: ci.Index, Volume: ci.Volume, Number: ci.Number, Name: ci.Name,
			})
		}

		j.state.Version = Version
		j.state.Chapters = next
		j.state.WithImages = req.WithImages
		j.state.Source = SourceRef{
			ID:           src.ID(),
			BookID:       req.BookID,
			EditionID:    editionID,
			EditionLabel: edition.Label(),
			BookURL:      book.URL,
		}
		j.state.Book = Metadata(book, edition)
		return j.save()
	}(); err != nil {
		return nil, err
	}

	if req.WithImages && book.CoverURL != "" {
		if err := j.fetchCover(ctx, src, book.CoverURL); err != nil {
			j.warn(fmt.Sprintf("cover download failed: %v", err))
		}
	}
	return j, nil
}

// resolveEdition settles an unspecified translation. An empty identifier is a
// valid one for sources that leave a translation unnamed, so it is kept when the
// book really has such an edition; otherwise the fullest one is taken, which is
// what a caller that did not choose almost always means.
func resolveEdition(book *novel.Book, editionID string) string {
	if editionID != "" || len(book.Editions) == 0 {
		return editionID
	}
	if e, ok := book.Edition(""); ok && e.Chapters > 0 {
		return ""
	}

	best := ""
	bestCount := 0
	for _, e := range book.Editions {
		if e.Chapters > bestCount {
			best, bestCount = e.ID, e.Chapters
		}
	}
	return best
}

// Metadata assembles book metadata from the source's details and the chosen translation.
func Metadata(b *novel.Book, edition novel.Edition) epub.Metadata {
	language := b.Language
	if language == "" {
		language = "en"
	}
	return epub.Metadata{
		Title:         b.Title,
		OriginalTitle: b.OriginalTitle,
		Language:      language,
		Authors:       b.Authors,
		Translators:   edition.Translators(),
		Genres:        b.Genres,
		Publisher:     b.Publisher,
		Date:          b.Year,
		Description:   b.Description,
		Source:        b.URL,
	}
}

// RefreshMetadata re-reads the book details. The chapter list and the chosen
// translation stay as they are.
func (j *Job) RefreshMetadata(ctx context.Context, src novel.Source) error {
	st := j.State()
	book, err := src.Book(ctx, st.Source.BookID)
	if err != nil {
		return err
	}
	edition, ok := book.Edition(st.Source.EditionID)
	if !ok {
		edition = novel.Edition{Teams: st.Book.Translators}
	}

	j.mu.Lock()
	defer j.mu.Unlock()
	j.state.Book = Metadata(book, edition)
	return j.save()
}

func (j *Job) fetchCover(ctx context.Context, src novel.Source, url string) error {
	data, _, err := src.Fetch(ctx, url)
	if err != nil {
		return err
	}
	ext := extOf(url)
	name := "cover." + ext
	if err := writeFileAtomic(j.assetPath(name), data); err != nil {
		return err
	}

	j.mu.Lock()
	defer j.mu.Unlock()
	j.state.Cover = &Asset{File: name, Ext: ext}
	return j.save()
}

// save writes job.json. Called with j.mu held.
func (j *Job) save() error {
	j.state.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(j.state, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(j.dir, "job.json"), data)
}

func (j *Job) warn(msg string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.state.Warnings = append(j.state.Warnings, msg)
}

// writeFileAtomic writes through a temporary file, so a crash cannot leave a broken job.json.
func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (j *Job) chapterPath(index int) string {
	return filepath.Join(j.dir, "chapters", fmt.Sprintf("%05d.json", index))
}

func (j *Job) assetPath(name string) string { return filepath.Join(j.dir, "assets", name) }

var unsafeName = regexp.MustCompile(`[^\w.-]+`)

func dirName(sourceID, bookID, editionID string) string {
	name := unsafeName.ReplaceAllString(sourceID+"-"+bookID, "_")
	if editionID == "" {
		return name + "--default"
	}
	return name + "--" + unsafeName.ReplaceAllString(editionID, "_")
}

// assetName produces a name that is stable across runs: it depends only on the address.
func assetName(url, ext string) string {
	sum := sha1.Sum([]byte(url))
	if ext == "" {
		ext = "jpg"
	}
	return "img-" + hex.EncodeToString(sum[:])[:12] + "." + ext
}

func extOf(u string) string {
	if e := markup.ExtOf(u); e != "" {
		return e
	}
	return "jpg"
}

// trimSpace exists so other files in the package need not import strings for one call.
func trimSpace(s string) string { return strings.TrimSpace(s) }
