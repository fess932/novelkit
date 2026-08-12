package markup_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/fess932/novelkit/markup"
	"github.com/fess932/novelkit/novel"
)

// parse mimics how content arrives inside an API response: a raw value whose
// shape is not known in advance.
func parse(raw string, att ...markup.Attachment) novel.Content {
	return markup.Auto(json.RawMessage(raw), att)
}

func TestProseMirrorXHTML(t *testing.T) {
	doc := `{"type":"doc","content":[
		{"type":"paragraph","content":[
			{"type":"text","text":"Обычный "},
			{"type":"text","marks":[{"type":"bold"}],"text":"жирный"},
			{"type":"text","text":" и "},
			{"type":"text","marks":[{"type":"italic"}],"text":"курсив"}
		]},
		{"type":"paragraph","content":[]},
		{"type":"horizontalRule"},
		{"type":"blockquote","content":[{"type":"paragraph","content":[{"type":"text","text":"цитата"}]}]}
	]}`

	got := parse(doc).XHTML(nil)
	for _, want := range []string{
		"<p>Обычный <strong>жирный</strong> и <em>курсив</em></p>",
		`<p class="empty">`,
		"<hr/>",
		"<blockquote>\n<p>цитата</p>\n</blockquote>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing fragment %q in:\n%s", want, got)
		}
	}
}

func TestProseMirrorImages(t *testing.T) {
	doc := `{"type":"doc","content":[
		{"type":"image","attrs":{"description":"Прим. пер.","images":[{"image":"pic"}]}},
		{"type":"paragraph","content":[{"type":"text","text":"текст"}]}
	]}`
	att := markup.Attachment{Name: "pic", Extension: "jpg", URL: "/uploads/pic.jpg", Width: 100, Height: 200}

	var seen []novel.Image
	got := parse(doc, att).XHTML(novel.ResolverFunc(func(img novel.Image) (string, bool) {
		seen = append(seen, img)
		return "../images/local.jpg", true
	}))

	if len(seen) != 1 || seen[0].URL != "/uploads/pic.jpg" || seen[0].Ext != "jpg" {
		t.Fatalf("the resolver never saw the picture: %+v", seen)
	}
	if !strings.Contains(got, `<img src="../images/local.jpg"`) {
		t.Errorf("no picture in the markup:\n%s", got)
	}
	// The caption is a translator's note and must survive.
	if !strings.Contains(got, "Прим. пер.") {
		t.Errorf("the illustration caption was lost:\n%s", got)
	}
}

func TestImagesDroppedWhenResolverRefuses(t *testing.T) {
	doc := `{"type":"doc","content":[{"type":"image","attrs":{"images":[{"image":"pic"}]}}]}`
	att := markup.Attachment{Name: "pic", URL: "/uploads/pic.jpg"}

	for name, resolver := range map[string]novel.ImageResolver{
		"declining resolver": novel.DropImages,
		"no resolver":        nil,
	} {
		if got := parse(doc, att).XHTML(resolver); strings.Contains(got, "<img") {
			t.Errorf("%s: the picture stayed in the markup:\n%s", name, got)
		}
	}
}

func TestHTMLContent(t *testing.T) {
	raw := `"<p data-paragraph-index=\"1\">Первый &laquo;абзац&raquo;</p><script>alert(1)</script>` +
		`<div class=\"x\">Развёрнутый <b>жирный</b></div><p>Не закрыт<em>курсив</p>"`

	got := parse(raw).XHTML(nil)
	checks := []struct {
		want string
		msg  string
	}{
		{"<p>Первый «абзац»</p>", "entities were not decoded"},
		{"<strong>жирный</strong>", "b did not become strong"},
		{"Развёрнутый", "text inside an unknown wrapper was lost"},
	}
	for _, c := range checks {
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: missing %q in:\n%s", c.msg, c.want, got)
		}
	}
	if strings.Contains(got, "alert") || strings.Contains(got, "data-paragraph") {
		t.Errorf("a script or bookkeeping attribute leaked into the markup:\n%s", got)
	}
	if strings.Count(got, "<em>") != strings.Count(got, "</em>") {
		t.Errorf("an unclosed tag was not closed:\n%s", got)
	}
}

// A book blurb arrives as a ProseMirror document. This used to put
// "[object Object]" into the metadata.
func TestPlainTextFromDocument(t *testing.T) {
	doc := `{"type":"doc","content":[
		{"type":"paragraph","content":[{"type":"text","text":"Первый абзац."}]},
		{"type":"paragraph","content":[{"type":"text","text":"Второй абзац."}]}
	]}`

	got := parse(doc).PlainText()
	want := "Первый абзац.\n\nВторой абзац."
	if got != want {
		t.Errorf("blurb parsed wrong:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestPlainTextFromHTML(t *testing.T) {
	got := parse(`"<p>Абзац&nbsp;один</p><p>Абзац два</p>"`).PlainText()
	if !strings.Contains(got, "Абзац один") || !strings.Contains(got, "Абзац два") {
		t.Errorf("text from html parsed wrong: %q", got)
	}
}

func TestEmptyContent(t *testing.T) {
	for _, raw := range []string{`null`, `""`, `{}`, `[]`} {
		c := parse(raw)
		if got := c.XHTML(nil); got != "" {
			t.Errorf("%s should give empty markup, got %q", raw, got)
		}
		if got := c.PlainText(); got != "" {
			t.Errorf("%s should give empty text, got %q", raw, got)
		}
	}
}

// A document that arrives as a string with JSON inside must parse as a document.
func TestDocumentInsideString(t *testing.T) {
	inner := `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"текст"}]}]}`
	wrapped, err := json.Marshal(inner)
	if err != nil {
		t.Fatal(err)
	}
	if got := markup.Auto(wrapped, nil).XHTML(nil); got != "<p>текст</p>" {
		t.Errorf("a document inside a string parsed as %q", got)
	}
}
