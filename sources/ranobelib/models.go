package ranobelib

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/fess932/novelkit/markup"
	"github.com/fess932/novelkit/novel"
)

// Attachment is a chapter attachment. The type lives in markup, which is what
// parses the markup and finds the pictures in it.
type Attachment = markup.Attachment

// Named is anything with a name of its own: a team, an author, a genre, a publisher.
type Named struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	RusName string `json:"rus_name"`
	Slug    string `json:"slug_url"`
}

// Title returns the Russian name when there is one, otherwise the original.
func (n Named) Title() string {
	if n.RusName != "" {
		return n.RusName
	}
	return n.Name
}

// Labeled covers fields shaped like {"id":2,"label":"Completed"}.
type Labeled struct {
	ID    int    `json:"id"`
	Label string `json:"label"`
}

// Cover is the book cover in several sizes.
type Cover struct {
	Filename  string `json:"filename"`
	Thumbnail string `json:"thumbnail"`
	Default   string `json:"default"`
	Md        string `json:"md"`
}

// URL returns the largest available version of the cover.
func (c Cover) URL() string {
	for _, u := range []string{c.Default, c.Md, c.Thumbnail} {
		if u != "" {
			return u
		}
	}
	return ""
}

// Manga is a book's details.
type Manga struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	RusName string `json:"rus_name"`
	EngName string `json:"eng_name"`
	Slug    string `json:"slug"`
	SlugURL string `json:"slug_url"`
	Cover   Cover  `json:"cover"`

	// SummaryRaw arrives as a ProseMirror document one time and a string the
	// next, so it is kept raw; Summary parses it.
	SummaryRaw  json.RawMessage `json:"summary"`
	Authors     []Named         `json:"authors"`
	Publisher   []Named         `json:"publisher"`
	Genres      []Named         `json:"genres"`
	Tags        []Named         `json:"tags"`
	ReleaseDate string          `json:"releaseDate"`
	Status      Labeled         `json:"status"`
	Type        Labeled         `json:"type"`
}

// Summary parses the book blurb.
func (m Manga) Summary() novel.Content { return markup.Auto(m.SummaryRaw, nil) }

// Title picks the book's name: Russian, else English, else the original.
func (m Manga) Title() string {
	for _, s := range []string{m.RusName, m.EngName, m.Name} {
		if s != "" {
			return s
		}
	}
	return m.SlugURL
}

// URL returns the link to the book on the site.
func (m Manga) URL(site string) string {
	return strings.TrimRight(site, "/") + "/ru/book/" + m.SlugURL
}

// AuthorNames returns author names ready for book metadata.
func (m Manga) AuthorNames() []string { return titles(m.Authors) }

// GenreNames returns the genre names.
func (m Manga) GenreNames() []string { return titles(m.Genres) }

func titles(list []Named) []string {
	out := make([]string, 0, len(list))
	for _, n := range list {
		if t := n.Title(); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// ChapterBranch is a translation branch as a chapter lists it. Only the team
// that posted this particular chapter appears here; Client.Branches has the
// full line-up.
type ChapterBranch struct {
	BranchID *int    `json:"branch_id"`
	Teams    []Named `json:"teams"`
	User     struct {
		ID       int    `json:"id"`
		Username string `json:"username"`
	} `json:"user"`
}

// ID returns the branch identifier; 0 means a branch without one (for a book
// with a single translation the site sends null).
func (b ChapterBranch) ID() int {
	if b.BranchID == nil {
		return 0
	}
	return *b.BranchID
}

// ChapterInfo is one entry of a book's chapter list.
type ChapterInfo struct {
	ID       int             `json:"id"`
	Index    int             `json:"index"`
	Volume   string          `json:"volume"`
	Number   string          `json:"number"`
	Name     string          `json:"name"`
	Branches []ChapterBranch `json:"branches"`
}

// InBranch reports whether this chapter exists in the given branch.
func (ci ChapterInfo) InBranch(branchID int) bool {
	for _, b := range ci.Branches {
		if b.ID() == branchID {
			return true
		}
	}
	return false
}

// Info converts the chapter to its common shape.
func (ci ChapterInfo) Info() novel.ChapterInfo {
	return novel.ChapterInfo{
		ID:     strconv.Itoa(ci.ID),
		Index:  ci.Index,
		Volume: ci.Volume,
		Number: ci.Number,
		Name:   ci.Name,
	}
}

// Title builds a readable chapter heading.
func (ci ChapterInfo) Title() string { return ci.Info().Title() }

// ChapterRef addresses a chapter: volume and number within the chosen branch.
type ChapterRef struct {
	Volume   string
	Number   string
	BranchID int // 0 leaves the branch unspecified
}

// Chapter is a chapter together with its text.
type Chapter struct {
	ID          int             `json:"id"`
	MangaID     int             `json:"manga_id"`
	Index       int             `json:"index"`
	Volume      string          `json:"volume"`
	Number      string          `json:"number"`
	Name        string          `json:"name"`
	BranchID    *int            `json:"branch_id"`
	Teams       []Named         `json:"teams"`
	ContentRaw  json.RawMessage `json:"content"`
	Attachments []Attachment    `json:"attachments"`
}

// Content parses the chapter text: the site sends a document one time and an HTML string the next.
func (c Chapter) Content() novel.Content { return markup.Auto(c.ContentRaw, c.Attachments) }

// Info converts the chapter to its common shape.
func (c Chapter) Info() novel.ChapterInfo {
	return novel.ChapterInfo{
		ID:     strconv.Itoa(c.ID),
		Index:  c.Index,
		Volume: c.Volume,
		Number: c.Number,
		Name:   c.Name,
	}
}

// Title builds the chapter heading.
func (c Chapter) Title() string { return c.Info().Title() }

// BranchCard is a branch as Client.Branches describes it: its own name and all
// of its teams. This is exactly how the tabs on the site are labelled.
type BranchCard struct {
	ID    int     `json:"id"`
	Name  string  `json:"name"`
	Teams []Named `json:"teams"`
}

// Branch is a translation branch merged from the chapter list and the branch cards.
type Branch struct {
	ID        int      // 0 means a branch without an identifier
	Name      string   // the site's internal name for the branch
	Teams     []string // translation teams
	Uploaders []string // the people who posted the chapters
	Count     int      // how many chapters the branch has
}

// Edition converts the branch to its common shape.
func (b Branch) Edition() novel.Edition {
	id := ""
	if b.ID != 0 {
		id = strconv.Itoa(b.ID)
	}
	return novel.Edition{
		ID:        id,
		Name:      b.Name,
		Teams:     b.Teams,
		Uploaders: b.Uploaders,
		Chapters:  b.Count,
	}
}

// Label is the branch caption for humans.
func (b Branch) Label() string { return b.Edition().Label() }

// Translators lists teams and uploaders, ready for book metadata.
func (b Branch) Translators() []string { return b.Edition().Translators() }
