package ranobelib

import (
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// Поля карточки книги, которые запрашиваются дополнительно.
var mangaFields = []string{"summary", "authors", "publisher", "genres", "tags", "teams", "releaseDate"}

// envelope снимает обёртку {"data": ...}, в которую сайт кладёт любой ответ.
func envelope[T any](op, u string, data []byte) (T, error) {
	var env struct {
		Data T `json:"data"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		var zero T
		return zero, &Error{Op: op, URL: u, Message: "не разобрать ответ", Err: err}
	}
	return env.Data, nil
}

// Search ищет книги по названию. Работает по подстроке: опечаток не прощает,
// зато понимает и русское, и оригинальное название.
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

// Manga отдаёт карточку книги. slug — вида "14841--beginning-after-the-end-novel".
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

// Chapters отдаёт список глав книги — сразу по всем веткам перевода.
func (c *Client) Chapters(ctx context.Context, slug string) ([]ChapterInfo, error) {
	u := c.apiURL + "/manga/" + url.PathEscape(slug) + "/chapters"

	body, _, err := c.get(ctx, "Chapters", u, "application/json")
	if err != nil {
		return nil, err
	}
	return envelope[[]ChapterInfo]("Chapters", u, body)
}

// Branches отдаёт карточки веток перевода по числовому id книги.
// Именно отсюда берутся подписи вкладок на сайте: у ветки бывает несколько команд,
// а в списке глав указана только та, что залила конкретную главу.
func (c *Client) Branches(ctx context.Context, mangaID int) ([]BranchCard, error) {
	u := c.apiURL + "/branches/" + strconv.Itoa(mangaID)

	body, _, err := c.get(ctx, "Branches", u, "application/json")
	if err != nil {
		return nil, err
	}
	return envelope[[]BranchCard]("Branches", u, body)
}

// Chapter отдаёт главу вместе с текстом.
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

// Fetch качает произвольный файл — обложку или иллюстрацию главы.
// Относительный путь достраивается до адреса сайта: на CDN обложек картинки глав отдают 403.
func (c *Client) Fetch(ctx context.Context, rawURL string) (data []byte, contentType string, err error) {
	return c.get(ctx, "Fetch", c.AbsoluteURL(rawURL), "*/*")
}

// AbsoluteURL достраивает относительный путь до полного адреса сайта.
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

// CollectBranches сводит ветки перевода: карточки дают названия команд,
// список глав — количество глав и тех, кто их заливал.
//
// Ветка, у которой на сайте есть вкладка, но нет ни одной главы, попадает
// в результат с Count == 0 — скачивать в ней нечего.
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
			// Команда главы — запасная подпись, если у ветки команды не проставлены.
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
	// Самая полная ветка первой — это почти всегда то, что нужно.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	return out
}

// ChaptersOfBranch отбирает главы одной ветки в порядке чтения.
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

// ParseSlug достаёт слаг книги из ссылки или возвращает строку как есть,
// если она уже похожа на слаг ("14841--beginning-after-the-end-novel").
func ParseSlug(input string) (string, bool) {
	s := strings.TrimSpace(input)
	if i := strings.Index(s, "ranobelib.me/"); i >= 0 {
		s = s[i+len("ranobelib.me/"):]
		for _, prefix := range []string{"ru/", "en/", "book/"} {
			s = strings.TrimPrefix(s, prefix)
		}
		if j := strings.IndexAny(s, "/?#"); j >= 0 {
			s = s[:j]
		}
		if unescaped, err := url.PathUnescape(s); err == nil {
			s = unescaped
		}
	}
	// Слаг сайта всегда начинается с числового идентификатора книги.
	id, rest, ok := strings.Cut(s, "--")
	if !ok || rest == "" {
		return "", false
	}
	if _, err := strconv.Atoi(id); err != nil {
		return "", false
	}
	return s, true
}
