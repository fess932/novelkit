package ranobelib

import (
	"fmt"
	"strings"

	"github.com/fess932/ranobelib/content"
)

// Attachment — вложение главы (иллюстрация). Тип живёт в пакете content,
// потому что именно он разбирает разметку и ищет в ней картинки.
type Attachment = content.Attachment

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

	// Summary приходит то ProseMirror-документом, то строкой,
	// поэтому хранится сырым и разбирается через content.
	Summary     content.Content `json:"summary"`
	Authors     []Named         `json:"authors"`
	Publisher   []Named         `json:"publisher"`
	Genres      []Named         `json:"genres"`
	Tags        []Named         `json:"tags"`
	ReleaseDate string          `json:"releaseDate"`
	Status      Labeled         `json:"status"`
	Type        Labeled         `json:"type"`
}

// Title — название для книги: русское, иначе английское, иначе оригинальное.
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

// Title собирает человекочитаемый заголовок главы: «Глава 1.2. Название».
func (ci ChapterInfo) Title() string {
	head := fmt.Sprintf("Глава %s", ci.Number)
	if name := strings.TrimSpace(ci.Name); name != "" {
		return head + ". " + name
	}
	return head
}

// Ref возвращает ссылку на главу для Client.Chapter.
func (ci ChapterInfo) Ref(branchID int) ChapterRef {
	return ChapterRef{Volume: ci.Volume, Number: ci.Number, BranchID: branchID}
}

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
	Volume      string          `json:"volume"`
	Number      string          `json:"number"`
	Name        string          `json:"name"`
	BranchID    *int            `json:"branch_id"`
	Teams       []Named         `json:"teams"`
	Content     content.Content `json:"content"`
	Attachments []Attachment    `json:"attachments"`
}

// Title собирает заголовок главы.
func (c Chapter) Title() string {
	return ChapterInfo{Number: c.Number, Name: c.Name}.Title()
}

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

// Label — подпись ветки для показа пользователю.
func (b Branch) Label() string {
	switch {
	case len(b.Teams) > 0:
		return strings.Join(b.Teams, " & ")
	case b.Name != "":
		return b.Name
	case len(b.Uploaders) > 0:
		return b.Uploaders[0]
	default:
		return "Неизвестный"
	}
}

// Translators — команды и заливавшие: годится для метаданных книги.
func (b Branch) Translators() []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(b.Teams)+len(b.Uploaders))
	for _, s := range append(append([]string{}, b.Teams...), b.Uploaders...) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		out = append(out, b.Label())
	}
	return out
}
