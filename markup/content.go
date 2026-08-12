// Package markup turns site markup into something a book can hold.
//
// These are ready-made building blocks for supporting a new site: HTML for
// sites that serve chapters as markup, ProseMirror for those that serve editor
// documents. Both implement novel.Content, so a source rarely has to write a
// parser of its own.
package markup

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/fess932/novelkit/novel"
)

// Attachment is a chapter attachment — an illustration the markup refers to.
type Attachment struct {
	Name      string `json:"name"`
	Filename  string `json:"filename"`
	Extension string `json:"extension"`
	URL       string `json:"url"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
}

// Empty is content with nothing in it.
type Empty struct{}

// XHTML implements novel.Content.
func (Empty) XHTML(novel.ImageResolver) string { return "" }

// PlainText implements novel.Content.
func (Empty) PlainText() string { return "" }

// Auto picks the parser by the shape of the value: an object is read as a
// ProseMirror document, a string as HTML. Sites built on the same engine send
// both, sometimes within a single book.
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
		// Sometimes the document arrives as a string with JSON inside.
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

// collapse squeezes blank lines and trims the edges. Non-breaking spaces become
// ordinary ones: they only get in the way of book metadata (a title search will
// not match them), while chapter markup keeps them as they are.
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
