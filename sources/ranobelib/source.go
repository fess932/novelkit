package ranobelib

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/fess932/novelkit/novel"
)

// SourceID is written into the job cache, so it must stay stable.
const SourceID = "ranobelib"

// Language is what the site serves: it is a Russian translation library.
const Language = "ru"

// Source plugs ranobelib.me into the core: it implements novel.Source.
//
// A book's chapter list is cached in memory for the lifetime of the source;
// otherwise fetching the book details and then one translation's chapters would
// ask the site for the same thing twice.
type Source struct {
	c *Client

	mu       sync.Mutex
	chapters map[string][]ChapterInfo
}

// NewSource creates a source on top of a fresh client.
func NewSource(opts ...Option) *Source {
	return SourceFor(New(opts...))
}

// SourceFor creates a source on top of an existing client, e.g. one with a
// swapped http.Client in tests.
func SourceFor(c *Client) *Source {
	return &Source{c: c, chapters: map[string][]ChapterInfo{}}
}

// Client exposes the API client: the site can do things the common interface cannot.
func (s *Source) Client() *Client { return s.c }

// ID implements novel.Source.
func (s *Source) ID() string { return SourceID }

// Supports implements novel.Source.
func (s *Source) Supports(rawURL string) bool {
	if strings.Contains(novel.NormalizeURL(rawURL), "ranobelib.me/") {
		return true
	}
	// A bare slug such as "14841--beginning-after-the-end-novel" is ours too.
	_, ok := ParseSlug(rawURL)
	return ok && !strings.Contains(rawURL, "://")
}

// ParseRef implements novel.Source.
func (s *Source) ParseRef(rawURL string) (string, bool) { return ParseSlug(rawURL) }

// Search implements novel.Source.
func (s *Source) Search(ctx context.Context, query string) ([]novel.Book, error) {
	found, err := s.c.Search(ctx, query)
	if err != nil {
		return nil, err
	}
	out := make([]novel.Book, 0, len(found))
	for _, m := range found {
		out = append(out, s.book(&m, nil))
	}
	return out, nil
}

// Book implements novel.Source: a book's details along with its translations.
func (s *Source) Book(ctx context.Context, bookID string) (*novel.Book, error) {
	manga, err := s.c.Manga(ctx, bookID)
	if err != nil {
		return nil, err
	}
	chapters, err := s.chapterList(ctx, bookID)
	if err != nil {
		return nil, err
	}
	// Branch cards are optional: without them the captions come from the chapters.
	cards, _ := s.c.Branches(ctx, manga.ID)

	book := s.book(manga, CollectBranches(chapters, cards))
	return &book, nil
}

func (s *Source) book(m *Manga, branches []Branch) novel.Book {
	title := m.Title()
	original := m.EngName
	if original == title {
		original = m.Name
	}
	publisher := ""
	if len(m.Publisher) > 0 {
		publisher = m.Publisher[0].Title()
	}

	editions := make([]novel.Edition, 0, len(branches))
	for _, b := range branches {
		editions = append(editions, b.Edition())
	}

	return novel.Book{
		ID:            m.SlugURL,
		Language:      Language,
		Title:         title,
		OriginalTitle: original,
		Authors:       m.AuthorNames(),
		Genres:        m.GenreNames(),
		Publisher:     publisher,
		Year:          m.ReleaseDate,
		Description:   m.Summary().PlainText(),
		CoverURL:      m.Cover.URL(),
		URL:           m.URL(s.c.SiteURL()),
		Editions:      editions,
	}
}

// Chapters implements novel.Source: one translation's chapters in reading order.
func (s *Source) Chapters(ctx context.Context, bookID, editionID string) ([]novel.ChapterInfo, error) {
	branchID, err := branchID(editionID)
	if err != nil {
		return nil, err
	}
	all, err := s.chapterList(ctx, bookID)
	if err != nil {
		return nil, err
	}

	out := make([]novel.ChapterInfo, 0, len(all))
	for _, ci := range ChaptersOfBranch(all, branchID) {
		out = append(out, ci.Info())
	}
	return out, nil
}

// Chapter implements novel.Source.
func (s *Source) Chapter(ctx context.Context, bookID, editionID string, ci novel.ChapterInfo) (*novel.Chapter, error) {
	id, err := branchID(editionID)
	if err != nil {
		return nil, err
	}
	ch, err := s.c.Chapter(ctx, bookID, ChapterRef{Volume: ci.Volume, Number: ci.Number, BranchID: id})
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(ch)
	if err != nil {
		return nil, err
	}
	// Only the chapter list knows the reading order, so carry it over from the request.
	info := ch.Info()
	info.Index = ci.Index
	return &novel.Chapter{Info: info, Content: ch.Content(), Raw: raw}, nil
}

// DecodeChapter implements novel.Source: it restores a chapter from the cache.
func (s *Source) DecodeChapter(raw []byte) (*novel.Chapter, error) {
	var ch Chapter
	if err := json.Unmarshal(raw, &ch); err != nil {
		return nil, fmt.Errorf("ranobelib: chapter from cache: %w", err)
	}
	return &novel.Chapter{Info: ch.Info(), Content: ch.Content(), Raw: raw}, nil
}

// Fetch implements novel.Source.
func (s *Source) Fetch(ctx context.Context, rawURL string) ([]byte, string, error) {
	return s.c.Fetch(ctx, rawURL)
}

// chapterList returns a book's chapter list, asking the site for it at most once.
func (s *Source) chapterList(ctx context.Context, bookID string) ([]ChapterInfo, error) {
	s.mu.Lock()
	cached, ok := s.chapters[bookID]
	s.mu.Unlock()
	if ok {
		return cached, nil
	}

	list, err := s.c.Chapters(ctx, bookID)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.chapters[bookID] = list
	s.mu.Unlock()
	return list, nil
}

// branchID turns a translation identifier into a branch number.
// An empty string means the branch without an identifier.
func branchID(editionID string) (int, error) {
	if editionID == "" {
		return 0, nil
	}
	id, err := strconv.Atoi(editionID)
	if err != nil {
		return 0, fmt.Errorf("ranobelib: invalid translation identifier %q", editionID)
	}
	return id, nil
}
