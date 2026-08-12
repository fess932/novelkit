package novel_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fess932/novelkit/novel"
)

// stubSource claims one host and reads a book id out of the last path segment.
type stubSource struct{ id, host string }

func (s stubSource) ID() string             { return s.id }
func (s stubSource) Supports(u string) bool { return strings.Contains(u, s.host) }
func (s stubSource) ParseRef(u string) (string, bool) {
	parts := strings.Split(strings.TrimSuffix(u, "/"), "/")
	last := parts[len(parts)-1]
	return last, strings.HasPrefix(last, "book-")
}
func (stubSource) Search(context.Context, string) ([]novel.Book, error) {
	return nil, novel.ErrUnsupported
}
func (stubSource) Book(context.Context, string) (*novel.Book, error) { return nil, novel.ErrNotFound }
func (stubSource) Chapters(context.Context, string, string) ([]novel.ChapterInfo, error) {
	return nil, nil
}
func (stubSource) Chapter(context.Context, string, string, novel.ChapterInfo) (*novel.Chapter, error) {
	return nil, novel.ErrNotFound
}
func (stubSource) DecodeChapter([]byte) (*novel.Chapter, error) { return nil, novel.ErrNotFound }
func (stubSource) Fetch(context.Context, string) ([]byte, string, error) {
	return nil, "", novel.ErrNotFound
}

// An unknown site and an unreadable address ask different things of the user, so
// they must be told apart.
func TestResolveDistinguishesFailures(t *testing.T) {
	var r novel.Registry
	r.Register(stubSource{id: "stub", host: "stub.test"})

	src, id, err := r.Resolve("https://stub.test/x/book-42")
	if err != nil || id != "book-42" || src == nil {
		t.Fatalf("a good link should resolve: %v, %q", err, id)
	}

	if _, _, err := r.Resolve("https://elsewhere.test/book-42"); !errors.Is(err, novel.ErrUnsupported) {
		t.Errorf("an unknown site must give ErrUnsupported, got %v", err)
	}
	if errors.Is(err, novel.ErrBadReference) {
		t.Error("an unknown site must not look like a bad address")
	}

	src, _, err = r.Resolve("https://stub.test/search")
	if !errors.Is(err, novel.ErrBadReference) {
		t.Errorf("a link without a book id must give ErrBadReference, got %v", err)
	}
	if errors.Is(err, novel.ErrUnsupported) {
		t.Error("a bad address must not look like an unknown site")
	}
	// The matched source comes back anyway, so a caller can name the site.
	if src == nil || src.ID() != "stub" {
		t.Errorf("the matched source should be returned alongside the error: %v", src)
	}
}
