package ranobelib

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/fess932/novelkit/markup"
	"github.com/fess932/novelkit/novel"
)

// Attachment — вложение главы. Тип живёт в markup: именно он разбирает
// разметку и ищет в ней картинки.
type Attachment = markup.Attachment

// Named — сущность со своим именем: команда, автор, жанр, издатель.
type Named struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	RusName string `json:"rus_name"`
	Slug    string `json:"slug_url"`
}

// Title возвращает русское имя, если оно есть, иначе оригинальное.
func (n Named) Title() string {
	if n.RusName != "" {
		return n.RusName
	}
	return n.Name
}

// Labeled — поля вида {"id":2,"label":"Завершён"}.
type Labeled struct {
	ID    int    `json:"id"`
	Label string `json:"label"`
}

// Cover — обложка книги в нескольких размерах.
type Cover struct {
	Filename  string `json:"filename"`
	Thumbnail string `json:"thumbnail"`
	Default   string `json:"default"`
	Md        string `json:"md"`
}

// URL возвращает самую крупную доступную версию обложки.
func (c Cover) URL() string {
	for _, u := range []string{c.Default, c.Md, c.Thumbnail} {
		if u != "" {
			return u
		}
	}
	return ""
}

// Manga — карточка книги.
type Manga struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	RusName string `json:"rus_name"`
	EngName string `json:"eng_name"`
	Slug    string `json:"slug"`
	SlugURL string `json:"slug_url"`
	Cover   Cover  `json:"cover"`

	// SummaryRaw приходит то ProseMirror-документом, то строкой,
	// поэтому хранится сырым; разбирает его Summary.
	SummaryRaw  json.RawMessage `json:"summary"`
	Authors     []Named         `json:"authors"`
	Publisher   []Named         `json:"publisher"`
	Genres      []Named         `json:"genres"`
	Tags        []Named         `json:"tags"`
	ReleaseDate string          `json:"releaseDate"`
	Status      Labeled         `json:"status"`
	Type        Labeled         `json:"type"`
}

// Summary разбирает аннотацию книги.
func (m Manga) Summary() novel.Content { return markup.Auto(m.SummaryRaw, nil) }

// Title — название книги: русское, иначе английское, иначе оригинальное.
func (m Manga) Title() string {
	for _, s := range []string{m.RusName, m.EngName, m.Name} {
		if s != "" {
			return s
		}
	}
	return m.SlugURL
}

// URL возвращает ссылку на книгу на сайте.
func (m Manga) URL(site string) string {
	return strings.TrimRight(site, "/") + "/ru/book/" + m.SlugURL
}

// AuthorNames возвращает имена авторов в удобном для метаданных виде.
func (m Manga) AuthorNames() []string { return titles(m.Authors) }

// GenreNames возвращает названия жанров.
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

// ChapterBranch — ветка перевода в том виде, в каком она указана у главы.
// Здесь перечислена только команда, залившая эту главу; полный состав ветки
// отдаёт Client.Branches.
type ChapterBranch struct {
	BranchID *int    `json:"branch_id"`
	Teams    []Named `json:"teams"`
	User     struct {
		ID       int    `json:"id"`
		Username string `json:"username"`
	} `json:"user"`
}

// ID возвращает идентификатор ветки; 0 означает ветку без идентификатора
// (у книги с единственным переводом сайт присылает null).
func (b ChapterBranch) ID() int {
	if b.BranchID == nil {
		return 0
	}
	return *b.BranchID
}

// ChapterInfo — глава в списке глав книги.
type ChapterInfo struct {
	ID       int             `json:"id"`
	Index    int             `json:"index"`
	Volume   string          `json:"volume"`
	Number   string          `json:"number"`
	Name     string          `json:"name"`
	Branches []ChapterBranch `json:"branches"`
}

// InBranch сообщает, есть ли эта глава в указанной ветке перевода.
func (ci ChapterInfo) InBranch(branchID int) bool {
	for _, b := range ci.Branches {
		if b.ID() == branchID {
			return true
		}
	}
	return false
}

// Info приводит главу к общему виду.
func (ci ChapterInfo) Info() novel.ChapterInfo {
	return novel.ChapterInfo{
		ID:     strconv.Itoa(ci.ID),
		Index:  ci.Index,
		Volume: ci.Volume,
		Number: ci.Number,
		Name:   ci.Name,
	}
}

// Title собирает человекочитаемый заголовок главы.
func (ci ChapterInfo) Title() string { return ci.Info().Title() }

// ChapterRef адресует главу: том и номер внутри выбранной ветки перевода.
type ChapterRef struct {
	Volume   string
	Number   string
	BranchID int // 0 — ветка не указывается
}

// Chapter — глава вместе с текстом.
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

// Content разбирает текст главы: сайт присылает то документ, то html-строку.
func (c Chapter) Content() novel.Content { return markup.Auto(c.ContentRaw, c.Attachments) }

// Info приводит главу к общему виду.
func (c Chapter) Info() novel.ChapterInfo {
	return novel.ChapterInfo{
		ID:     strconv.Itoa(c.ID),
		Index:  c.Index,
		Volume: c.Volume,
		Number: c.Number,
		Name:   c.Name,
	}
}

// Title собирает заголовок главы.
func (c Chapter) Title() string { return c.Info().Title() }

// BranchCard — карточка ветки перевода из Client.Branches:
// собственное имя ветки и все её команды. Именно так подписаны вкладки на сайте.
type BranchCard struct {
	ID    int     `json:"id"`
	Name  string  `json:"name"`
	Teams []Named `json:"teams"`
}

// Branch — ветка перевода, сведённая из списка глав и карточек веток.
type Branch struct {
	ID        int      // 0 — ветка без идентификатора
	Name      string   // внутреннее имя ветки на сайте
	Teams     []string // команды перевода
	Uploaders []string // те, кто заливал главы
	Count     int      // сколько глав в ветке
}

// Edition приводит ветку к общему виду.
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

// Label — подпись ветки для показа пользователю.
func (b Branch) Label() string { return b.Edition().Label() }

// Translators — команды и заливавшие: годится для метаданных книги.
func (b Branch) Translators() []string { return b.Edition().Translators() }
