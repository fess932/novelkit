// Package novel holds the types and the source interface shared by every site.
//
// The core knows nothing about any particular site. A site is plugged in by
// implementing Source (see sources/ranobelib), its markup is normalised by the
// markup package, and the book is assembled by the epub package. Caching and
// resumable downloads (package job) work the same way for every source.
package novel

import (
	"fmt"
	"strings"
)

// Image is a picture referenced by chapter markup.
type Image struct {
	URL  string // address on the site; may be relative
	Name string // attachment name, for sites that list pictures separately
	Ext  string
	// Description is the caption. On some sites it carries a translator's note.
	Description string
	Width       int
	Height      int
}

// ImageResolver decides what happens to every picture: either it returns the
// path to use inside the book, or it declines (ok == false) and the picture
// disappears from the markup.
//
// An implementation may record the pictures it is asked about; that is how
// they are discovered before being downloaded.
type ImageResolver interface {
	Resolve(Image) (path string, ok bool)
}

// ResolverFunc adapts a plain function to ImageResolver.
type ResolverFunc func(Image) (string, bool)

// Resolve implements ImageResolver.
func (f ResolverFunc) Resolve(img Image) (string, bool) { return f(img) }

// DropImages removes every picture from the markup.
var DropImages ImageResolver = ResolverFunc(func(Image) (string, bool) { return "", false })

// Content is chapter content normalised to a common shape.
//
// Every site stores its text differently: a ProseMirror document here, HTML
// there, something else elsewhere. Implementing this interface is all it takes
// for that text to end up in a book.
type Content interface {
	// XHTML returns the chapter body: block elements without the html and body wrappers.
	XHTML(ImageResolver) string
	// PlainText returns the same text with no markup.
	PlainText() string
}

// Edition is one translation of a book: a "branch" on one site, a "team" on another.
type Edition struct {
	ID    string // identifier within the source; may be empty
	Name  string // the site's own name for it
	Teams []string
	// Uploaders are the people who posted the chapters.
	Uploaders []string
	// Chapters counts the chapters in this translation. Zero means there is
	// nothing to download.
	Chapters int
}

// Label is the edition's caption for humans.
func (e Edition) Label() string {
	switch {
	case len(e.Teams) > 0:
		return strings.Join(e.Teams, " & ")
	case e.Name != "":
		return e.Name
	case len(e.Uploaders) > 0:
		return e.Uploaders[0]
	default:
		return "Unknown"
	}
}

// Translators lists teams and uploaders, ready for book metadata.
func (e Edition) Translators() []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(e.Teams)+len(e.Uploaders))
	for _, s := range append(append([]string{}, e.Teams...), e.Uploaders...) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		out = append(out, e.Label())
	}
	return out
}

// Book describes a title.
type Book struct {
	// ID identifies the book within its source: a slug, a numeric key, anything.
	ID            string
	Title         string
	OriginalTitle string
	Authors       []string
	Genres        []string
	Publisher     string
	Year          string
	// Description is the blurb, as plain text.
	Description string
	CoverURL    string
	URL         string
	// Editions lists the available translations. An empty list means the
	// source has no notion of them.
	Editions []Edition
}

// Edition looks a translation up by its identifier.
func (b *Book) Edition(id string) (Edition, bool) {
	for _, e := range b.Editions {
		if e.ID == id {
			return e, true
		}
	}
	return Edition{}, false
}

// ChapterInfo is one entry of a chapter list.
type ChapterInfo struct {
	// ID identifies the chapter within its source.
	ID string
	// Index defines the reading order.
	Index  int
	Volume string
	Number string
	Name   string
}

// Title builds a readable heading, e.g. "Chapter 1.2. The Name".
func (ci ChapterInfo) Title() string { return ci.TitleWith("Chapter") }

// TitleWith builds the heading with a custom word for "chapter", so that a book
// in another language does not end up with an English heading.
func (ci ChapterInfo) TitleWith(word string) string {
	head := word + " " + ci.Number
	if ci.Number == "" {
		head = fmt.Sprintf("%s %d", word, ci.Index)
	}
	if name := strings.TrimSpace(ci.Name); name != "" {
		return head + ". " + name
	}
	return head
}

// Chapter is a chapter together with its text.
type Chapter struct {
	Info    ChapterInfo
	Content Content
	// Raw is the site's own response. The cache stores exactly this and turns it
	// back into a chapter through Source.DecodeChapter, so fixing the parser
	// never requires downloading anything again.
	Raw []byte
}
