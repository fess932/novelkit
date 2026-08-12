package novel

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// ErrNotFound reports that a book or chapter does not exist: a wrong id,
// a removed chapter, or one that is behind a paywall.
var ErrNotFound = errors.New("novel: not found")

// ErrUnsupported reports that no registered source handles this link, or that a
// source does not implement the requested operation. Nothing the user can fix by
// editing the address.
var ErrUnsupported = errors.New("novel: unsupported")

// ErrBadReference reports the opposite case: a source does handle this site, but
// the address carries no book identifier it can use — a link to the front page,
// to a search, or one that got truncated. Worth telling the user to check the
// address, which is why it is separate from ErrUnsupported.
var ErrBadReference = errors.New("novel: no book identifier in the link")

// Source is a site to download books from.
//
// Implementing this interface is all that supporting a new site takes; caching,
// resumable downloads, EPUB assembly and image compression are already written
// and behave the same for every source.
//
// An implementation must pace its own requests: the core does not do it, and
// sites cut off clients that hammer them.
type Source interface {
	// ID is a short name for the source, e.g. "ranobelib". It is written into
	// the job cache, so it must stay stable.
	ID() string

	// Supports reports whether this source handles the given link.
	Supports(rawURL string) bool

	// ParseRef extracts a book identifier from a link to the site.
	ParseRef(rawURL string) (bookID string, ok bool)

	// Search looks books up by title. A source without search may return
	// ErrUnsupported.
	Search(ctx context.Context, query string) ([]Book, error)

	// Book returns the title's details along with its translations.
	Book(ctx context.Context, bookID string) (*Book, error)

	// Chapters returns the chapters of one translation, in reading order.
	Chapters(ctx context.Context, bookID, editionID string) ([]ChapterInfo, error)

	// Chapter downloads a single chapter together with its text.
	Chapter(ctx context.Context, bookID, editionID string, ci ChapterInfo) (*Chapter, error)

	// DecodeChapter restores a chapter from a stored Chapter.Raw.
	DecodeChapter(raw []byte) (*Chapter, error)

	// Fetch downloads a file referenced by the markup: a cover or an
	// illustration. The source resolves relative addresses itself.
	Fetch(ctx context.Context, rawURL string) (data []byte, contentType string, err error)
}

// Registry is a set of plugged-in sources.
// The zero value is ready to use and safe for concurrent access.
type Registry struct {
	mu      sync.RWMutex
	sources map[string]Source
	order   []string
}

// Register adds a source. Registering the same ID again replaces the previous one.
func (r *Registry) Register(s Source) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sources == nil {
		r.sources = map[string]Source{}
	}
	if _, exists := r.sources[s.ID()]; !exists {
		r.order = append(r.order, s.ID())
	}
	r.sources[s.ID()] = s
}

// Get returns a source by its identifier.
func (r *Registry) Get(id string) (Source, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.sources[id]
	return s, ok
}

// For picks the source that handles the given link.
func (r *Registry) For(rawURL string) (Source, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, id := range r.order {
		if s := r.sources[id]; s.Supports(rawURL) {
			return s, true
		}
	}
	return nil, false
}

// Sources lists the registered sources in registration order.
func (r *Registry) Sources() []Source {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Source, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, r.sources[id])
	}
	return out
}

// Resolve parses a link: it finds the source and extracts the book identifier.
//
// The two ways this fails call for different answers, so they are different
// errors: ErrUnsupported means no source claims the link at all, while
// ErrBadReference means one did but found no book identifier in it. In the
// second case the matched source is still returned, so a caller can name the
// site it was talking about.
func (r *Registry) Resolve(rawURL string) (Source, string, error) {
	s, ok := r.For(rawURL)
	if !ok {
		return nil, "", fmt.Errorf("%w: %s", ErrUnsupported, rawURL)
	}
	id, ok := s.ParseRef(rawURL)
	if !ok {
		return s, "", fmt.Errorf("%w: %s (source %s)", ErrBadReference, rawURL, s.ID())
	}
	return s, id, nil
}

// SearchAll queries every source at once and collects the results per source.
// Sources without search are skipped; an error comes back only when nothing
// answered at all.
func (r *Registry) SearchAll(ctx context.Context, query string) (map[string][]Book, error) {
	sources := r.Sources()

	var (
		mu      sync.Mutex
		out     = make(map[string][]Book, len(sources))
		errs    []error
		wg      sync.WaitGroup
		anyGood bool
	)
	for _, s := range sources {
		wg.Add(1)
		go func(s Source) {
			defer wg.Done()
			books, err := s.Search(ctx, query)

			mu.Lock()
			defer mu.Unlock()
			switch {
			case errors.Is(err, ErrUnsupported):
				return
			case err != nil:
				errs = append(errs, fmt.Errorf("%s: %w", s.ID(), err))
			default:
				anyGood = true
				if len(books) > 0 {
					out[s.ID()] = books
				}
			}
		}(s)
	}
	wg.Wait()

	if !anyGood && len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return out, nil
}

// SortEditions puts the translations with the most chapters first, which is
// almost always the one worth offering.
func SortEditions(list []Edition) {
	sort.SliceStable(list, func(i, j int) bool { return list[i].Chapters > list[j].Chapters })
}

// SortChapters puts chapters into reading order.
func SortChapters(list []ChapterInfo) {
	sort.SliceStable(list, func(i, j int) bool { return list[i].Index < list[j].Index })
}

// NormalizeURL trims the scheme and www prefix so hosts can be compared.
func NormalizeURL(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	return strings.TrimPrefix(s, "www.")
}
