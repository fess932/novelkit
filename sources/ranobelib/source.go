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

// ID источника. Попадает в кэш заданий, поэтому менять его нельзя.
const SourceID = "ranobelib"

// Source подключает ranobelib.me к ядру: реализует novel.Source.
//
// Список глав книги кэшируется в памяти на время жизни источника: карточка
// книги и выборка глав перевода иначе дёргали бы сайт дважды за одно и то же.
type Source struct {
	c *Client

	mu       sync.Mutex
	chapters map[string][]ChapterInfo
}

// NewSource создаёт источник поверх нового клиента.
func NewSource(opts ...Option) *Source {
	return SourceFor(New(opts...))
}

// SourceFor создаёт источник поверх готового клиента —
// например, с подменённым http.Client в тестах.
func SourceFor(c *Client) *Source {
	return &Source{c: c, chapters: map[string][]ChapterInfo{}}
}

// Client отдаёт клиент API: у сайта есть возможности, которых нет в общем интерфейсе.
func (s *Source) Client() *Client { return s.c }

// ID реализует novel.Source.
func (s *Source) ID() string { return SourceID }

// Supports реализует novel.Source.
func (s *Source) Supports(rawURL string) bool {
	if strings.Contains(novel.NormalizeURL(rawURL), "ranobelib.me/") {
		return true
	}
	// Голый слаг вида "14841--beginning-after-the-end-novel" тоже наш.
	_, ok := ParseSlug(rawURL)
	return ok && !strings.Contains(rawURL, "://")
}

// ParseRef реализует novel.Source.
func (s *Source) ParseRef(rawURL string) (string, bool) { return ParseSlug(rawURL) }

// Search реализует novel.Source.
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

// Book реализует novel.Source: карточка книги вместе со списком переводов.
func (s *Source) Book(ctx context.Context, bookID string) (*novel.Book, error) {
	manga, err := s.c.Manga(ctx, bookID)
	if err != nil {
		return nil, err
	}
	chapters, err := s.chapterList(ctx, bookID)
	if err != nil {
		return nil, err
	}
	// Карточки веток необязательны: без них останутся подписи по главам.
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

// Chapters реализует novel.Source: главы выбранного перевода в порядке чтения.
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

// Chapter реализует novel.Source.
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
	// Порядок чтения знает только список глав, поэтому переносим его из запроса.
	info := ch.Info()
	info.Index = ci.Index
	return &novel.Chapter{Info: info, Content: ch.Content(), Raw: raw}, nil
}

// DecodeChapter реализует novel.Source: восстанавливает главу из кэша.
func (s *Source) DecodeChapter(raw []byte) (*novel.Chapter, error) {
	var ch Chapter
	if err := json.Unmarshal(raw, &ch); err != nil {
		return nil, fmt.Errorf("ranobelib: глава из кэша: %w", err)
	}
	return &novel.Chapter{Info: ch.Info(), Content: ch.Content(), Raw: raw}, nil
}

// Fetch реализует novel.Source.
func (s *Source) Fetch(ctx context.Context, rawURL string) ([]byte, string, error) {
	return s.c.Fetch(ctx, rawURL)
}

// chapterList отдаёт список глав книги, запрашивая его не чаще одного раза.
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

// branchID переводит идентификатор перевода в номер ветки.
// Пустая строка означает ветку без идентификатора.
func branchID(editionID string) (int, error) {
	if editionID == "" {
		return 0, nil
	}
	id, err := strconv.Atoi(editionID)
	if err != nil {
		return 0, fmt.Errorf("ranobelib: неверный идентификатор перевода %q", editionID)
	}
	return id, nil
}
