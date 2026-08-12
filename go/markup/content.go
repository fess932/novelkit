// Package markup приводит разметку сайтов к виду, пригодному для книги.
//
// Здесь лежат готовые кирпичи, из которых собирается поддержка нового сайта:
// HTML — для сайтов, отдающих главу разметкой, ProseMirror — для тех, кто
// отдаёт документ редактора. Оба реализуют novel.Content, поэтому источнику
// достаточно выбрать подходящий и не писать разбор заново.
package markup

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/fess932/novelkit/novel"
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

// Empty — пустое содержимое.
type Empty struct{}

// XHTML реализует novel.Content.
func (Empty) XHTML(novel.ImageResolver) string { return "" }

// PlainText реализует novel.Content.
func (Empty) PlainText() string { return "" }

// Auto выбирает разбор по форме значения: объект разбирается как
// ProseMirror-документ, строка — как HTML. Сайты на одном движке присылают
// то одно, то другое даже в пределах одной книги.
func Auto(raw json.RawMessage, attachments []Attachment) novel.Content {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || bytes.Equal(t, []byte("null")) {
		return Empty{}
	}

	if t[0] == '"' {
		var s string
		if err := json.Unmarshal(t, &s); err != nil {
			return Empty{}
		}
		s = strings.TrimSpace(s)
		// Иногда документ приезжает строкой с JSON внутри.
		if strings.HasPrefix(s, "{") {
			if doc := ProseMirror(json.RawMessage(s), attachments); !isEmptyDoc(doc) {
				return doc
			}
		}
		if s == "" {
			return Empty{}
		}
		return HTML(s)
	}

	if t[0] == '{' {
		if doc := ProseMirror(t, attachments); !isEmptyDoc(doc) {
			return doc
		}
	}
	return Empty{}
}

func isEmptyDoc(c novel.Content) bool {
	doc, ok := c.(*Document)
	return !ok || doc == nil || doc.root.Type == ""
}

// collapse убирает лишние переводы строк и пробелы по краям.
// Неразрывные пробелы становятся обычными: в метаданных книги они только мешают
// (поиск по названию их не находит), а в разметке главы сохраняются как есть.
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

func resolve(r novel.ImageResolver, img novel.Image) (string, bool) {
	if r == nil {
		return "", false
	}
	return r.Resolve(img)
}
