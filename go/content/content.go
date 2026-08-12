// Package content разбирает содержимое главы и аннотацию книги.
//
// Сайт отдаёт их в двух разных видах: ProseMirror-документом (JSON) и
// HTML-строкой. Оба приводятся к одному чистому XHTML, пригодному для EPUB:
// неизвестные теги разворачиваются без потери текста, скрипты и стили
// выбрасываются, незакрытые теги закрываются.
package content

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Attachment — вложение главы: иллюстрация, на которую ссылается разметка.
type Attachment struct {
	Name      string `json:"name"`
	Filename  string `json:"filename"`
	Extension string `json:"extension"`
	URL       string `json:"url"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
}

// Image — картинка, найденная в разметке.
type Image struct {
	URL  string // адрес на сайте, может быть относительным
	Name string // имя вложения, если картинка пришла вложением
	Ext  string
	// Description — подпись из ProseMirror-узла: там часто лежит примечание переводчика.
	Description string
	Width       int
	Height      int
}

// ImageResolver решает судьбу каждой картинки: вернуть путь внутри книги
// или отказаться (ok == false), и тогда картинка из разметки просто исчезнет.
//
// Реализация может попутно запоминать картинки — так их и находят перед скачиванием.
type ImageResolver interface {
	Resolve(Image) (path string, ok bool)
}

// ResolverFunc позволяет использовать обычную функцию как ImageResolver.
type ResolverFunc func(Image) (string, bool)

// Resolve реализует ImageResolver.
func (f ResolverFunc) Resolve(img Image) (string, bool) { return f(img) }

// DropImages выбрасывает все картинки из разметки.
var DropImages ImageResolver = ResolverFunc(func(Image) (string, bool) { return "", false })

// Options управляют разбором содержимого.
type Options struct {
	// Attachments — вложения главы; по ним ProseMirror-узлы находят адреса картинок.
	Attachments []Attachment
	// Images решает, что делать с найденными картинками. nil — выбросить все.
	Images ImageResolver
}

// Content — сырое содержимое главы или аннотации.
// Хранится как есть, чтобы разбор был явным шагом: сайт меняет форму этого поля.
type Content struct {
	raw json.RawMessage
}

// New оборачивает сырой JSON.
func New(raw json.RawMessage) Content { return Content{raw: raw} }

// FromString создаёт содержимое из готовой HTML- или текстовой строки.
func FromString(s string) Content {
	b, _ := json.Marshal(s)
	return Content{raw: b}
}

// Raw отдаёт сырое представление — например, чтобы сложить его в кэш.
func (c Content) Raw() json.RawMessage { return c.raw }

// UnmarshalJSON сохраняет значение любой формы: объект, строку или null.
func (c *Content) UnmarshalJSON(b []byte) error {
	c.raw = append(c.raw[:0], b...)
	return nil
}

// MarshalJSON возвращает содержимое в исходном виде.
func (c Content) MarshalJSON() ([]byte, error) {
	if len(c.raw) == 0 {
		return []byte("null"), nil
	}
	return c.raw, nil
}

// IsZero сообщает, что содержимого нет.
func (c Content) IsZero() bool {
	t := bytes.TrimSpace(c.raw)
	return len(t) == 0 || bytes.Equal(t, []byte("null")) || bytes.Equal(t, []byte(`""`))
}

// kind разбирает сырое значение: либо документ ProseMirror, либо строка.
func (c Content) kind() (doc *node, text string) {
	t := bytes.TrimSpace(c.raw)
	if len(t) == 0 {
		return nil, ""
	}

	if t[0] == '"' {
		var s string
		if err := json.Unmarshal(t, &s); err != nil {
			return nil, ""
		}
		s = strings.TrimSpace(s)
		// Иногда документ приезжает строкой с JSON внутри.
		if strings.HasPrefix(s, "{") {
			var n node
			if err := json.Unmarshal([]byte(s), &n); err == nil && n.Type != "" {
				return &n, ""
			}
		}
		return nil, s
	}

	if t[0] == '{' {
		var n node
		if err := json.Unmarshal(t, &n); err == nil && n.Type != "" {
			return &n, ""
		}
	}
	return nil, ""
}

// XHTML собирает фрагмент тела главы: набор блочных элементов без обёрток.
// Пустое содержимое даёт пустую строку, ошибок не возвращает — испорченная
// разметка всегда приводится к чему-то отображаемому.
func (c Content) XHTML(opt Options) string {
	doc, text := c.kind()
	switch {
	case doc != nil:
		return strings.TrimSpace(renderDoc(doc, opt))
	case text != "":
		return strings.TrimSpace(renderHTML(text, opt))
	default:
		return ""
	}
}

// PlainText вытаскивает простой текст — для аннотации книги и поиска.
func (c Content) PlainText() string {
	doc, text := c.kind()
	switch {
	case doc != nil:
		return collapse(plainDoc(doc))
	case text != "":
		return collapse(plainHTML(text))
	default:
		return ""
	}
}

// collapse убирает лишние переводы строк и пробелы по краям.
// Неразрывные пробелы становятся обычными: в метаданных книги они только мешают
// (поиск по названию их не находит), а в разметке главы они сохраняются как есть.
func collapse(s string) string {
	s = strings.ReplaceAll(s, " ", " ")
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, l := range lines {
		l = strings.TrimRight(l, " \t")
		if strings.TrimSpace(l) == "" {
			blank++
			if blank > 1 {
				continue
			}
			out = append(out, "")
			continue
		}
		blank = 0
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// attachmentIndex ускоряет поиск вложения по имени.
func attachmentIndex(list []Attachment) map[string]Attachment {
	idx := make(map[string]Attachment, len(list)*2)
	for _, a := range list {
		if a.Name != "" {
			idx[a.Name] = a
		}
		if a.Filename != "" {
			idx[a.Filename] = a
		}
	}
	return idx
}

func resolve(opt Options, img Image) (string, bool) {
	if opt.Images == nil {
		return "", false
	}
	return opt.Images.Resolve(img)
}
