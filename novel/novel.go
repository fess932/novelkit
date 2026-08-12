// Package novel — ядро: общие для всех сайтов типы и интерфейс источника.
//
// Ядро ничего не знает про конкретные сайты. Сайт подключается реализацией
// Source (см. sources/ranobelib), разметка приводится к общему виду через
// пакет markup, а книга собирается пакетом epub. Кэш и докачка (пакет job)
// работают с любым источником одинаково.
package novel

import (
	"fmt"
	"strings"
)

// Image — картинка, найденная в разметке главы.
type Image struct {
	URL  string // адрес на сайте, может быть относительным
	Name string // имя вложения, если сайт присылает картинки отдельным списком
	Ext  string
	// Description — подпись: у некоторых сайтов там лежит примечание переводчика.
	Description string
	Width       int
	Height      int
}

// ImageResolver решает судьбу каждой картинки: вернуть путь внутри книги
// или отказаться (ok == false) — тогда картинка из разметки просто исчезнет.
//
// Реализация может попутно запоминать картинки: так их и находят перед скачиванием.
type ImageResolver interface {
	Resolve(Image) (path string, ok bool)
}

// ResolverFunc позволяет использовать обычную функцию как ImageResolver.
type ResolverFunc func(Image) (string, bool)

// Resolve реализует ImageResolver.
func (f ResolverFunc) Resolve(img Image) (string, bool) { return f(img) }

// DropImages выбрасывает все картинки из разметки.
var DropImages ImageResolver = ResolverFunc(func(Image) (string, bool) { return "", false })

// Content — содержимое главы, приведённое к общему виду.
//
// Каждый сайт хранит текст по-своему: у одного ProseMirror-документ, у другого
// HTML, у третьего что угодно ещё. Реализация этого интерфейса — единственное,
// что нужно, чтобы содержимое попало в книгу.
type Content interface {
	// XHTML отдаёт фрагмент тела главы: блочные элементы без обёрток html и body.
	XHTML(ImageResolver) string
	// PlainText отдаёт тот же текст без разметки.
	PlainText() string
}

// Edition — вариант перевода книги: «ветка» у одного сайта, «команда» у другого.
type Edition struct {
	ID    string // идентификатор внутри источника; пустой допустим
	Name  string // внутреннее название на сайте
	Teams []string
	// Uploaders — те, кто заливал главы.
	Uploaders []string
	// Chapters — сколько глав в этом переводе. Ноль означает, что качать нечего.
	Chapters int
}

// Label — подпись перевода для показа пользователю.
func (e Edition) Label() string {
	switch {
	case len(e.Teams) > 0:
		return strings.Join(e.Teams, " & ")
	case e.Name != "":
		return e.Name
	case len(e.Uploaders) > 0:
		return e.Uploaders[0]
	default:
		return "Неизвестный"
	}
}

// Translators — команды и заливавшие: годится для метаданных книги.
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

// Book — карточка книги.
type Book struct {
	// ID — идентификатор внутри источника: слаг, числовой ключ, что угодно.
	ID            string
	Title         string
	OriginalTitle string
	Authors       []string
	Genres        []string
	Publisher     string
	Year          string
	// Description — аннотация простым текстом.
	Description string
	CoverURL    string
	URL         string
	// Editions — доступные переводы. Пустой список означает,
	// что у источника переводов как таковых нет.
	Editions []Edition
}

// Edition ищет перевод по идентификатору.
func (b *Book) Edition(id string) (Edition, bool) {
	for _, e := range b.Editions {
		if e.ID == id {
			return e, true
		}
	}
	return Edition{}, false
}

// ChapterInfo — глава в списке глав.
type ChapterInfo struct {
	// ID — идентификатор главы внутри источника.
	ID string
	// Index задаёт порядок чтения.
	Index  int
	Volume string
	Number string
	Name   string
}

// Title собирает человекочитаемый заголовок: «Глава 1.2. Название».
func (ci ChapterInfo) Title() string {
	head := "Глава " + ci.Number
	if ci.Number == "" {
		head = fmt.Sprintf("Глава %d", ci.Index)
	}
	if name := strings.TrimSpace(ci.Name); name != "" {
		return head + ". " + name
	}
	return head
}

// Chapter — глава вместе с текстом.
type Chapter struct {
	Info    ChapterInfo
	Content Content
	// Raw — сырой ответ сайта. Кэш хранит именно его, а обратно в главу
	// его превращает тот же источник через DecodeChapter: так починка разбора
	// не требует перекачивать уже скачанное.
	Raw []byte
}
