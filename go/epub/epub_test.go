package epub_test

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"strings"
	"testing"

	"github.com/fess932/ranobelib/epub"
)

func sample() *epub.Book {
	return &epub.Book{
		Metadata: epub.Metadata{
			Title:         "Тестовая книга",
			OriginalTitle: "Test Book",
			Authors:       []string{"Автор"},
			Translators:   []string{"Команда", "Переводчик"},
			Genres:        []string{"Фэнтези"},
			Publisher:     "Издатель",
			Date:          "2015",
			Description:   "Первый абзац.\n\nВторой абзац.",
			Source:        "https://ranobelib.me/ru/book/1--test",
		},
		Cover: &epub.Image{Name: "cover.jpg", MediaType: "image/jpeg", Data: []byte("jpegdata")},
		Chapters: []epub.Chapter{
			{Volume: "1", Number: "1", Title: "Глава 1. Начало", Body: "<p>Текст</p>"},
			{Volume: "2", Number: "2", Title: "Глава 2. Продолжение", Body: `<div class="img"><img src="../images/a.jpg" alt=""/></div>`},
		},
		Images: []epub.Image{{Name: "a.jpg", MediaType: "image/jpeg", Data: []byte("imagedata")}},
	}
}

func open(t *testing.T, b *epub.Book) *zip.Reader {
	t.Helper()
	data, err := b.Bytes()
	if err != nil {
		t.Fatalf("сборка книги: %v", err)
	}
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("книга не открывается как zip: %v", err)
	}
	return r
}

func read(t *testing.T, r *zip.Reader, name string) string {
	t.Helper()
	f, err := r.Open(name)
	if err != nil {
		t.Fatalf("нет файла %s: %v", name, err)
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// EPUB требует, чтобы mimetype лежал первым и без сжатия.
func TestMimetypeFirstAndStored(t *testing.T) {
	r := open(t, sample())

	first := r.File[0]
	if first.Name != "mimetype" {
		t.Fatalf("первым в архиве лежит %q, а должен mimetype", first.Name)
	}
	if first.Method != zip.Store {
		t.Errorf("mimetype сжат, а должен лежать как есть")
	}
	if first.Flags&0x8 != 0 {
		t.Errorf("у mimetype выставлен флаг дескриптора данных — валидаторы на это ругаются")
	}
	if got := read(t, r, "mimetype"); got != "application/epub+zip" {
		t.Errorf("содержимое mimetype: %q", got)
	}
}

func TestEveryXMLIsWellFormed(t *testing.T) {
	r := open(t, sample())
	checked := 0
	for _, f := range r.File {
		if !strings.HasSuffix(f.Name, ".xhtml") && !strings.HasSuffix(f.Name, ".opf") &&
			!strings.HasSuffix(f.Name, ".ncx") && !strings.HasSuffix(f.Name, ".xml") {
			continue
		}
		checked++
		dec := xml.NewDecoder(strings.NewReader(read(t, r, f.Name)))
		dec.Strict = true
		dec.Entity = xml.HTMLEntity
		for {
			_, err := dec.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Errorf("%s: невалидный XML: %v", f.Name, err)
				break
			}
		}
	}
	if checked < 6 {
		t.Errorf("проверено подозрительно мало файлов: %d", checked)
	}
}

func TestManifestMatchesArchive(t *testing.T) {
	r := open(t, sample())
	opf := read(t, r, "OEBPS/content.opf")

	inArchive := map[string]bool{}
	for _, f := range r.File {
		inArchive[strings.TrimPrefix(f.Name, "OEBPS/")] = true
	}
	for _, href := range hrefs(opf) {
		if !inArchive[href] {
			t.Errorf("в манифесте есть %s, а файла в архиве нет", href)
		}
	}

	for _, want := range []string{
		"<dc:title>Тестовая книга</dc:title>",
		"<dc:creator id=\"creator-0\">Автор</dc:creator>",
		"<dc:contributor id=\"contrib-0\">Команда</dc:contributor>",
		"<dc:publisher>Издатель</dc:publisher>",
		`properties="cover-image"`,
		`<meta name="cover" content="cover-image"/>`,
	} {
		if !strings.Contains(opf, want) {
			t.Errorf("в content.opf нет %q", want)
		}
	}
	// Аннотация должна попасть и в метаданные, и на титульную страницу.
	if !strings.Contains(opf, "<dc:description>Первый абзац.") {
		t.Errorf("аннотация не попала в метаданные")
	}
	if title := read(t, r, "OEBPS/text/title.xhtml"); !strings.Contains(title, "Аннотация") {
		t.Errorf("аннотация не попала на титульную страницу")
	}
}

// Тома в оглавлении группируются, если томов больше одного.
func TestNavGroupsVolumes(t *testing.T) {
	r := open(t, sample())
	nav := read(t, r, "OEBPS/nav.xhtml")

	for _, want := range []string{"<li><span>Том 1</span>", "<li><span>Том 2</span>", "Глава 1. Начало"} {
		if !strings.Contains(nav, want) {
			t.Errorf("в оглавлении нет %q", want)
		}
	}
	ncx := read(t, r, "OEBPS/toc.ncx")
	if strings.Count(ncx, "<navPoint") != 3 { // титул + две главы
		t.Errorf("в toc.ncx неверное число пунктов:\n%s", ncx)
	}
}

func TestBookWithoutChaptersFails(t *testing.T) {
	b := &epub.Book{Metadata: epub.Metadata{Title: "Пусто"}}
	if _, err := b.Bytes(); err == nil {
		t.Fatal("книга без глав должна давать ошибку")
	}
}

func hrefs(opf string) []string {
	var out []string
	for _, part := range strings.Split(opf, `href="`)[1:] {
		if i := strings.Index(part, `"`); i > 0 {
			out = append(out, part[:i])
		}
	}
	return out
}
