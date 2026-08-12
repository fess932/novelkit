package markup

import (
	"encoding/json"
	"fmt"
	"html"
	"strings"

	"github.com/fess932/novelkit/novel"
)

// Document is chapter content stored as a ProseMirror editor document.
type Document struct {
	root        node
	attachments []Attachment
}

// ProseMirror parses an editor document. Broken JSON yields an empty document
// rather than an error: one malformed chapter must not sink the whole book.
func ProseMirror(raw json.RawMessage, attachments []Attachment) novel.Content {
	var n node
	if err := json.Unmarshal(raw, &n); err != nil {
		return &Document{}
	}
	return &Document{root: n, attachments: attachments}
}

// node is a document node. Attributes are kept raw and decoded lazily: sites
// add fields to them as they please, and strict decoding would break on the
// first unfamiliar book.
type node struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Attrs   json.RawMessage `json:"attrs"`
	Content []node          `json:"content"`
	Marks   []node          `json:"marks"`
}

type nodeAttrs struct {
	Level       int    `json:"level"`
	Href        string `json:"href"`
	Description string `json:"description"`
	Images      []struct {
		Image string `json:"image"`
	} `json:"images"`
}

func (n node) attrs() nodeAttrs {
	var a nodeAttrs
	if len(n.Attrs) > 0 {
		_ = json.Unmarshal(n.Attrs, &a) // junk in attributes is no reason to lose text
	}
	return a
}

// markTags lists the text styling worth carrying into a book.
var markTags = map[string]string{
	"bold":          "strong",
	"strong":        "strong",
	"italic":        "em",
	"em":            "em",
	"underline":     "u",
	"strike":        "s",
	"strikethrough": "s",
	"superscript":   "sup",
	"subscript":     "sub",
	"code":          "code",
}

// XHTML implements novel.Content.
func (d *Document) XHTML(images novel.ImageResolver) string {
	if d == nil || d.root.Type == "" {
		return ""
	}
	r := &pmRenderer{images: images, att: attachmentIndex(d.attachments)}
	var b strings.Builder
	r.node(&b, d.root)
	return strings.TrimSpace(b.String())
}

// PlainText implements novel.Content.
func (d *Document) PlainText() string {
	if d == nil || d.root.Type == "" {
		return ""
	}
	var b strings.Builder
	var walk func(node)
	walk = func(cur node) {
		switch cur.Type {
		case "text":
			b.WriteString(cur.Text)
		case "hardBreak":
			b.WriteString("\n")
		}
		for _, c := range cur.Content {
			walk(c)
		}
		switch cur.Type {
		case "paragraph", "heading", "blockquote", "listItem":
			b.WriteString("\n\n")
		}
	}
	walk(d.root)
	return collapse(b.String())
}

type pmRenderer struct {
	images novel.ImageResolver
	att    map[string]Attachment
}

func (r *pmRenderer) children(b *strings.Builder, n node) {
	for _, c := range n.Content {
		r.node(b, c)
	}
}

func (r *pmRenderer) node(b *strings.Builder, n node) {
	switch n.Type {
	case "doc":
		r.children(b, n)

	case "text":
		b.WriteString(r.text(n))

	case "paragraph":
		var inner strings.Builder
		r.children(&inner, n)
		if s := strings.TrimSpace(inner.String()); s != "" {
			b.WriteString("<p>" + s + "</p>\n")
		} else {
			// An empty paragraph is a scene break on these sites, so keep it.
			b.WriteString("<p class=\"empty\"> </p>\n")
		}

	case "heading":
		level := min(max(n.attrs().Level, 2), 6)
		fmt.Fprintf(b, "<h%d>", level)
		r.children(b, n)
		fmt.Fprintf(b, "</h%d>\n", level)

	case "blockquote":
		b.WriteString("<blockquote>\n")
		r.children(b, n)
		b.WriteString("</blockquote>\n")

	case "bulletList":
		b.WriteString("<ul>\n")
		r.children(b, n)
		b.WriteString("</ul>\n")

	case "orderedList":
		b.WriteString("<ol>\n")
		r.children(b, n)
		b.WriteString("</ol>\n")

	case "listItem":
		b.WriteString("<li>")
		r.children(b, n)
		b.WriteString("</li>\n")

	case "codeBlock":
		b.WriteString("<pre><code>")
		r.children(b, n)
		b.WriteString("</code></pre>\n")

	case "horizontalRule":
		b.WriteString("<hr/>\n")

	case "hardBreak":
		b.WriteString("<br/>")

	case "image":
		r.image(b, n)

	default:
		// Unwrap unknown nodes: the text inside matters more than the wrapper.
		r.children(b, n)
	}
}

func (r *pmRenderer) text(n node) string {
	out := esc(n.Text)
	// Marks are listed outermost first, so wrap them in reverse.
	for i := len(n.Marks) - 1; i >= 0; i-- {
		m := n.Marks[i]
		if m.Type == "link" {
			if href := m.attrs().Href; href != "" && safeHref(href) {
				out = `<a href="` + esc(href) + `">` + out + `</a>`
			}
			continue
		}
		if tag, ok := markTags[m.Type]; ok {
			out = "<" + tag + ">" + out + "</" + tag + ">"
		}
	}
	return out
}

func (r *pmRenderer) image(b *strings.Builder, n node) {
	a := n.attrs()
	for _, ref := range a.Images {
		att, ok := r.att[ref.Image]
		if !ok {
			continue
		}
		path, ok := resolve(r.images, novel.Image{
			URL:         att.URL,
			Name:        att.Name,
			Ext:         att.Extension,
			Description: a.Description,
			Width:       att.Width,
			Height:      att.Height,
		})
		if !ok {
			continue
		}
		b.WriteString(`<div class="img"><img src="` + esc(path) + `" alt=""/></div>` + "\n")
	}
	// An illustration caption often holds a translator's note: that is text, and it must survive.
	if d := strings.TrimSpace(a.Description); d != "" {
		b.WriteString(`<p class="note">` + esc(d) + "</p>\n")
	}
}

// esc escapes text for XML. html.EscapeString emits numeric references rather
// than named ones, which is exactly what XHTML wants.
func esc(s string) string { return html.EscapeString(stripInvalidXML(s)) }

func safeHref(href string) bool {
	return !strings.HasPrefix(strings.ToLower(strings.TrimSpace(href)), "javascript:")
}

// stripInvalidXML drops control characters that XML 1.0 forbids.
func stripInvalidXML(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t', r == '\n', r == '\r':
			return r
		case r < 0x20, r == 0xFFFE, r == 0xFFFF:
			return -1
		default:
			return r
		}
	}, s)
}
