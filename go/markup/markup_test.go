package markup_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/fess932/ranobelib/markup"
	"github.com/fess932/ranobelib/novel"
)

// parse имитирует то, как содержимое приезжает внутри ответа API:
// сырым значением, форма которого заранее неизвестна.
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
			t.Errorf("нет фрагмента %q в:\n%s", want, got)
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
		t.Fatalf("картинка до резолвера не дошла: %+v", seen)
	}
	if !strings.Contains(got, `<img src="../images/local.jpg"`) {
		t.Errorf("нет картинки в разметке:\n%s", got)
	}
	// Подпись — это примечание переводчика, терять его нельзя.
	if !strings.Contains(got, "Прим. пер.") {
		t.Errorf("потеряна подпись к иллюстрации:\n%s", got)
	}
}

func TestImagesDroppedWhenResolverRefuses(t *testing.T) {
	doc := `{"type":"doc","content":[{"type":"image","attrs":{"images":[{"image":"pic"}]}}]}`
	att := markup.Attachment{Name: "pic", URL: "/uploads/pic.jpg"}

	for name, resolver := range map[string]novel.ImageResolver{
		"отказ":         novel.DropImages,
		"нет резолвера": nil,
	} {
		if got := parse(doc, att).XHTML(resolver); strings.Contains(got, "<img") {
			t.Errorf("%s: картинка осталась в разметке:\n%s", name, got)
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
		{"<p>Первый «абзац»</p>", "сущности не раскрыты"},
		{"<strong>жирный</strong>", "b не превратился в strong"},
		{"Развёрнутый", "текст неизвестной обёртки потерян"},
	}
	for _, c := range checks {
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: нет %q в:\n%s", c.msg, c.want, got)
		}
	}
	if strings.Contains(got, "alert") || strings.Contains(got, "data-paragraph") {
		t.Errorf("в разметку просочился скрипт или служебный атрибут:\n%s", got)
	}
	if strings.Count(got, "<em>") != strings.Count(got, "</em>") {
		t.Errorf("незакрытый тег не закрылся:\n%s", got)
	}
}

// Аннотация книги приезжает документом ProseMirror. Раньше на этом месте
// в метаданные попадало "[object Object]".
func TestPlainTextFromDocument(t *testing.T) {
	doc := `{"type":"doc","content":[
		{"type":"paragraph","content":[{"type":"text","text":"Первый абзац."}]},
		{"type":"paragraph","content":[{"type":"text","text":"Второй абзац."}]}
	]}`

	got := parse(doc).PlainText()
	want := "Первый абзац.\n\nВторой абзац."
	if got != want {
		t.Errorf("аннотация разобрана неверно:\nполучено: %q\nожидалось: %q", got, want)
	}
}

func TestPlainTextFromHTML(t *testing.T) {
	got := parse(`"<p>Абзац&nbsp;один</p><p>Абзац два</p>"`).PlainText()
	if !strings.Contains(got, "Абзац один") || !strings.Contains(got, "Абзац два") {
		t.Errorf("текст из html разобран неверно: %q", got)
	}
}

func TestEmptyContent(t *testing.T) {
	for _, raw := range []string{`null`, `""`, `{}`, `[]`} {
		c := parse(raw)
		if got := c.XHTML(nil); got != "" {
			t.Errorf("для %s ожидалась пустая разметка, получено %q", raw, got)
		}
		if got := c.PlainText(); got != "" {
			t.Errorf("для %s ожидался пустой текст, получено %q", raw, got)
		}
	}
}

// Документ, приехавший строкой с JSON внутри, должен разбираться как документ.
func TestDocumentInsideString(t *testing.T) {
	inner := `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"текст"}]}]}`
	wrapped, err := json.Marshal(inner)
	if err != nil {
		t.Fatal(err)
	}
	if got := markup.Auto(wrapped, nil).XHTML(nil); got != "<p>текст</p>" {
		t.Errorf("документ в строке разобран как %q", got)
	}
}
