package content_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/fess932/ranobelib/content"
)

// unmarshal имитирует то, как содержимое приезжает внутри ответа API.
func unmarshal(t *testing.T, raw string) content.Content {
	t.Helper()
	var c content.Content
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatalf("разбор содержимого: %v", err)
	}
	return c
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

	got := unmarshal(t, doc).XHTML(content.Options{})
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
	att := []content.Attachment{{Name: "pic", Extension: "jpg", URL: "/uploads/pic.jpg", Width: 100, Height: 200}}

	var seen []content.Image
	got := unmarshal(t, doc).XHTML(content.Options{
		Attachments: att,
		Images: content.ResolverFunc(func(img content.Image) (string, bool) {
			seen = append(seen, img)
			return "../images/local.jpg", true
		}),
	})

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
	att := []content.Attachment{{Name: "pic", URL: "/uploads/pic.jpg"}}

	got := unmarshal(t, doc).XHTML(content.Options{Attachments: att, Images: content.DropImages})
	if strings.Contains(got, "<img") {
		t.Errorf("картинка осталась, хотя резолвер отказался:\n%s", got)
	}
}

func TestHTMLContent(t *testing.T) {
	raw := `"<p data-paragraph-index=\"1\">Первый &laquo;абзац&raquo;</p><script>alert(1)</script>` +
		`<div class=\"x\">Развёрнутый <b>жирный</b></div><p>Не закрыт<em>курсив</p>"`

	got := unmarshal(t, raw).XHTML(content.Options{})
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

	got := unmarshal(t, doc).PlainText()
	want := "Первый абзац.\n\nВторой абзац."
	if got != want {
		t.Errorf("аннотация разобрана неверно:\nполучено: %q\nождалось: %q", got, want)
	}
}

func TestPlainTextFromHTML(t *testing.T) {
	got := unmarshal(t, `"<p>Абзац&nbsp;один</p><p>Абзац два</p>"`).PlainText()
	if !strings.Contains(got, "Абзац один") || !strings.Contains(got, "Абзац два") {
		t.Errorf("текст из html разобран неверно: %q", got)
	}
}

func TestEmptyContent(t *testing.T) {
	for _, raw := range []string{`null`, `""`, `{}`} {
		c := unmarshal(t, raw)
		if got := c.XHTML(content.Options{}); got != "" {
			t.Errorf("для %s ожидалась пустая разметка, получено %q", raw, got)
		}
		if got := c.PlainText(); got != "" {
			t.Errorf("для %s ожидался пустой текст, получено %q", raw, got)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	raw := `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"a"}]}]}`
	c := unmarshal(t, raw)
	out, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != raw {
		t.Errorf("содержимое не сохранилось дословно:\n%s", out)
	}
}
