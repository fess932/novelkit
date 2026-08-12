package ranobelib

import (
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// Extra fields requested along with a book's details.
var mangaFields = []string{"summary", "authors", "publisher", "genres", "tags", "teams", "releaseDate"}

// envelope strips the {"data": ...} wrapper the site puts around every response.
func envelope[T any](op, u string, data []byte) (T, error) {
	var env struct {
		Data T `json:"data"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		var zero T
		return zero, &Error{Op: op, URL: u, Message: "cannot decode the response", Err: err}
	}
	return env.Data, nil
}

// Search looks books up by title. It matches substrings: typos are not
// forgiven, but both the Russian and the original title work.
func (c *Client) Search(ctx context.Context, query string) ([]Manga, error) {
	u := c.apiURL + "/manga?" + url.Values{
		"q":         {query},
		"site_id[]": {c.siteID},
	}.Encode()

	body, _, err := c.get(ctx, "Search", u, "application/json")
	if err != nil {
		return nil, err
	}
	return envelope[[]Manga]("Search", u, body)
}

// Manga returns a book's details. slug looks like "14841--beginning-after-the-end-novel".
func (c *Client) Manga(ctx context.Context, slug string) (*Manga, error) {
	q := make([]string, 0, len(mangaFields))
	for _, f := range mangaFields {
		q = append(q, "fields[]="+f)
	}
	u := c.apiURL + "/manga/" + url.PathEscape(slug) + "?" + strings.Join(q, "&")

	body, _, err := c.get(ctx, "Manga", u, "application/json")
	if err != nil {
		return nil, err
	}
	m, err := envelope[Manga]("Manga", u, body)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// Chapters returns a book's chapter list, covering every translation branch at once.
func (c *Client) Chapters(ctx context.Context, slug string) ([]ChapterInfo, error) {
	u := c.apiURL + "/manga/" + url.PathEscape(slug) + "/chapters"

	body, _, err := c.get(ctx, "Chapters", u, "application/json")
	if err != nil {
		return nil, err
	}
	return envelope[[]ChapterInfo]("Chapters", u, body)
}

// Branches returns the branch cards for a numeric book id. This is where the
// site's tab captions come from: a branch may have several teams, while the
// chapter list names only the one that posted a given chapter.
func (c *Client) Branches(ctx context.Context, mangaID int) ([]BranchCard, error) {
	u := c.apiURL + "/branches/" + strconv.Itoa(mangaID)

	body, _, err := c.get(ctx, "Branches", u, "application/json")
	if err != nil {
		return nil, err
	}
	return envelope[[]BranchCard]("Branches", u, body)
}

// Chapter returns a chapter together with its text.
func (c *Client) Chapter(ctx context.Context, slug string, ref ChapterRef) (*Chapter, error) {
	v := url.Values{"volume": {ref.Volume}, "number": {ref.Number}}
	if ref.BranchID != 0 {
		v.Set("branch_id", strconv.Itoa(ref.BranchID))
	}
	u := c.apiURL + "/manga/" + url.PathEscape(slug) + "/chapter?" + v.Encode()

	body, _, err := c.get(ctx, "Chapter", u, "application/json")
	if err != nil {
		return nil, err
	}
	ch, err := envelope[Chapter]("Chapter", u, body)
	if err != nil {
		return nil, err
	}
	return &ch, nil
}

// Fetch downloads an arbitrary file: a cover or a chapter illustration.
// Relative paths are resolved against the site, because the cover CDN answers
// 403 for chapter pictures.
func (c *Client) Fetch(ctx context.Context, rawURL string) (data []byte, contentType string, err error) {
	return c.get(ctx, "Fetch", c.AbsoluteURL(rawURL), "*/*")
}

// AbsoluteURL resolves a relative path against the site address.
func (c *Client) AbsoluteURL(raw string) string {
	switch {
	case strings.HasPrefix(raw, "http://"), strings.HasPrefix(raw, "https://"):
		return raw
	case strings.HasPrefix(raw, "//"):
		return "https:" + raw
	case strings.HasPrefix(raw, "/"):
		return c.siteURL + raw
	default:
		return c.siteURL + "/" + raw
	}
}

// CollectBranches merges translation branches: the cards supply team names,
// the chapter list supplies the counts and the uploaders.
//
// A branch that has a tab on the site but no chapters at all comes back with
// Count == 0 — there is nothing to download there.
func CollectBranches(chapters []ChapterInfo, cards []BranchCard) []Branch {
	order := make([]int, 0, len(cards)+4)
	byID := make(map[int]*Branch, len(cards)+4)

	get := func(id int) *Branch {
		b, ok := byID[id]
		if !ok {
			b = &Branch{ID: id}
			byID[id] = b
			order = append(order, id)
		}
		return b
	}

	for _, card := range cards {
		b := get(card.ID)
		b.Name = card.Name
		for _, t := range card.Teams {
			b.Teams = appendUnique(b.Teams, t.Title())
		}
	}

	for _, ch := range chapters {
		for _, cb := range ch.Branches {
			b := get(cb.ID())
			b.Count++
			// A chapter's team is the fallback caption when the branch lists none.
			for _, t := range cb.Teams {
				b.Teams = appendUnique(b.Teams, t.Title())
			}
			b.Uploaders = appendUnique(b.Uploaders, cb.User.Username)
		}
	}

	out := make([]Branch, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	// The fullest branch first: that is almost always the one wanted.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	return out
}

// ChaptersOfBranch selects one branch's chapters in reading order.
func ChaptersOfBranch(chapters []ChapterInfo, branchID int) []ChapterInfo {
	out := make([]ChapterInfo, 0, len(chapters))
	for _, ch := range chapters {
		if ch.InBranch(branchID) {
			out = append(out, ch)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out
}

func appendUnique(list []string, v string) []string {
	if v == "" {
		return list
	}
	for _, s := range list {
		if s == v {
			return list
		}
	}
	return append(list, v)
}

// ParseSlug extracts a book slug from a link, or passes the string through when
// it already looks like one ("14841--beginning-after-the-end-novel").
//
// Any path shape works: the site serves the same book under /ru/book/<slug>,
// /ru/manga/<slug> and other sections, so the first path segment shaped like
// <digits>--<rest> wins rather than a fixed list of known prefixes.
func ParseSlug(input string) (string, bool) {
	s := strings.TrimSpace(input)
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	for seg := range strings.SplitSeq(s, "/") {
		if seg == "" {
			continue
		}
		if unescaped, err := url.PathUnescape(seg); err == nil {
			seg = unescaped
		}
		if looksLikeSlug(seg) {
			return seg, true
		}
	}
	return "", false
}

// looksLikeSlug reports whether a path segment is a book slug: on this site one
// always starts with the numeric book id followed by "--".
func looksLikeSlug(seg string) bool {
	id, rest, ok := strings.Cut(seg, "--")
	if !ok || id == "" || rest == "" {
		return false
	}
	_, err := strconv.Atoi(id)
	return err == nil
}
