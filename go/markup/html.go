package markup

import (
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/fess932/novelkit/novel"
)

// HTML — содержимое главы в виде разметки.
//
// Неизвестные теги разворачиваются (текст не теряется), скрипты и стили
// выбрасываются, незакрытые теги закрываются, служебные атрибуты сайта
// не переносятся.
type HTML string

// allowedTags — теги, которые имеет смысл нести в книгу, и во что они превращаются.
var allowedTags = map[string]string{
	"p": "p", "br": "br", "hr": "hr",
	"b": "strong", "strong": "strong",
	"i": "em", "em": "em",
	"u": "u", "s": "s", "strike": "s", "del": "del", "ins": "ins",
	"sup": "sup", "sub": "sub",
	"blockquote": "blockquote",
	"h1":         "h2", "h2": "h2", "h3": "h3", "h4": "h4", "h5": "h5", "h6": "h6",
	"ul": "ul", "ol": "ol", "li": "li",
	"a": "a", "img": "img",
	"code": "code", "pre": "pre",
	"table": "table", "thead": "thead", "tbody": "tbody",
	"tr": "tr", "td": "td", "th": "th",
	"figure": "figure", "figcaption": "figcaption",
}

// voidTags закрываются сами: в XHTML это <br/>, а не <br>.
var voidTags = map[string]bool{"br": true, "hr": true, "img": true}

// dropTags выбрасываются вместе с содержимым.
var dropTags = map[string]bool{
	"script": true, "style": true, "iframe": true, "noscript": true,
	"svg": true, "form": true, "button": true, "input": true, "head": true,
}

// blockTags разделяют текст в PlainText.
var blockTags = map[string]bool{
	"p": true, "div": true, "li": true, "blockquote": true, "figcaption": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true, "tr": true,
}

func parseFragment(s string) []*html.Node {
	body := &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
	nodes, err := html.ParseFragment(strings.NewReader(s), body)
	if err != nil {
		return nil
	}
	return nodes
}

// XHTML реализует novel.Content.
func (h HTML) XHTML(images novel.ImageResolver) string {
	r := &htmlRenderer{images: images}
	var b strings.Builder
	for _, n := range parseFragment(string(h)) {
		r.node(&b, n)
	}
	return strings.TrimSpace(b.String())
}

// PlainText реализует novel.Content.
func (h HTML) PlainText() string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		switch n.Type {
		case html.TextNode:
			b.WriteString(n.Data)
		case html.ElementNode:
			name := strings.ToLower(n.Data)
			if dropTags[name] {
				return
			}
			if name == "br" {
				b.WriteString("\n")
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == html.ElementNode && blockTags[strings.ToLower(n.Data)] {
			b.WriteString("\n\n")
		}
	}
	for _, n := range parseFragment(string(h)) {
		walk(n)
	}
	return collapse(b.String())
}

type htmlRenderer struct {
	images novel.ImageResolver
}

func (r *htmlRenderer) children(b *strings.Builder, n *html.Node) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		r.node(b, c)
	}
}

func (r *htmlRenderer) node(b *strings.Builder, n *html.Node) {
	switch n.Type {
	case html.TextNode:
		b.WriteString(esc(n.Data))
		return
	case html.ElementNode:
		// продолжаем ниже
	default:
		// Комментарии, doctype и прочее в книгу не нужны.
		return
	}

	name := strings.ToLower(n.Data)
	if dropTags[name] {
		return
	}

	tag, ok := allowedTags[name]
	if !ok {
		r.children(b, n) // неизвестная обёртка — разворачиваем
		return
	}

	switch tag {
	case "img":
		r.image(b, n)
		return
	case "a":
		href := attr(n, "href")
		if href == "" || !safeHref(href) {
			r.children(b, n) // ссылка без адреса: текст оставляем, обёртку убираем
			return
		}
		b.WriteString(`<a href="` + esc(href) + `">`)
		r.children(b, n)
		b.WriteString("</a>")
		return
	}

	if voidTags[tag] {
		b.WriteString("<" + tag + "/>")
		return
	}

	b.WriteString("<" + tag + ">")
	r.children(b, n)
	b.WriteString("</" + tag + ">")
}

func (r *htmlRenderer) image(b *strings.Builder, n *html.Node) {
	src := attr(n, "src")
	if src == "" {
		src = attr(n, "data-src")
	}
	if src == "" {
		return
	}
	path, ok := resolve(r.images, novel.Image{URL: src, Ext: ExtOf(src)})
	if !ok {
		return
	}
	b.WriteString(`<div class="img"><img src="` + esc(path) + `" alt="` + esc(attr(n, "alt")) + `"/></div>` + "\n")
}

func attr(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, name) {
			return a.Val
		}
	}
	return ""
}

// ExtOf достаёт расширение файла из адреса.
func ExtOf(u string) string {
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	if i := strings.LastIndex(u, "."); i >= 0 && len(u)-i <= 6 {
		return strings.ToLower(u[i+1:])
	}
	return ""
}
