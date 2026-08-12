package epub_test

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"strings"
	"testing"

	"github.com/fess932/novelkit/epub"
)

func sample() *epub.Book {
	return &epub.Book{
		Metadata: epub.Metadata{
			Title:         "Test Book",
			OriginalTitle: "Test Book",
			Authors:       []string{"Author"},
			Translators:   []string{"Team", "Translator"},
			Genres:        []string{"Fantasy"},
			Publisher:     "Publisher",
			Date:          "2015",
			Description:   "First paragraph.\n\nSecond paragraph.",
			Source:        "https://ranobelib.me/ru/book/1--test",
		},
		Cover: &epub.Image{Name: "cover.jpg", MediaType: "image/jpeg", Data: []byte("jpegdata")},
		Chapters: []epub.Chapter{
			{Volume: "1", Number: "1", Title: "Chapter 1. The Beginning", Body: "<p>Text</p>"},
			{Volume: "2", Number: "2", Title: "Chapter 2. Onwards", Body: `<div class="img"><img src="../images/a.jpg" alt=""/></div>`},
		},
		Images: []epub.Image{{Name: "a.jpg", MediaType: "image/jpeg", Data: []byte("imagedata")}},
	}
}

func open(t *testing.T, b *epub.Book) *zip.Reader {
	t.Helper()
	data, err := b.Bytes()
	if err != nil {
		t.Fatalf("assembling the book: %v", err)
	}
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("the book does not open as a zip: %v", err)
	}
	return r
}

func read(t *testing.T, r *zip.Reader, name string) string {
	t.Helper()
	f, err := r.Open(name)
	if err != nil {
		t.Fatalf("no file %s: %v", name, err)
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// EPUB requires mimetype to come first and be stored uncompressed.
func TestMimetypeFirstAndStored(t *testing.T) {
	r := open(t, sample())

	first := r.File[0]
	if first.Name != "mimetype" {
		t.Fatalf("the first archive entry is %q, it must be mimetype", first.Name)
	}
	if first.Method != zip.Store {
		t.Errorf("mimetype is compressed, it must be stored")
	}
	if first.Flags&0x8 != 0 {
		t.Errorf("mimetype has the data descriptor flag set, which validators complain about")
	}
	if got := read(t, r, "mimetype"); got != "application/epub+zip" {
		t.Errorf("mimetype content: %q", got)
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
				t.Errorf("%s: invalid XML: %v", f.Name, err)
				break
			}
		}
	}
	if checked < 6 {
		t.Errorf("suspiciously few files checked: %d", checked)
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
			t.Errorf("the manifest lists %s but the archive has no such file", href)
		}
	}

	for _, want := range []string{
		"<dc:title>Test Book</dc:title>",
		"<dc:creator id=\"creator-0\">Author</dc:creator>",
		"<dc:contributor id=\"contrib-0\">Team</dc:contributor>",
		"<dc:publisher>Publisher</dc:publisher>",
		`properties="cover-image"`,
		`<meta name="cover" content="cover-image"/>`,
	} {
		if !strings.Contains(opf, want) {
			t.Errorf("content.opf is missing %q", want)
		}
	}
	// The blurb must reach both the metadata and the title page.
	if !strings.Contains(opf, "<dc:description>First paragraph.") {
		t.Errorf("the blurb never reached the metadata")
	}
	if title := read(t, r, "OEBPS/text/title.xhtml"); !strings.Contains(title, "Annotation") {
		t.Errorf("the blurb never reached the title page")
	}
}

// The contents group by volume when there is more than one.
func TestNavGroupsVolumes(t *testing.T) {
	r := open(t, sample())
	nav := read(t, r, "OEBPS/nav.xhtml")

	for _, want := range []string{"<li><span>Volume 1</span>", "<li><span>Volume 2</span>", "Chapter 1. The Beginning"} {
		if !strings.Contains(nav, want) {
			t.Errorf("the contents are missing %q", want)
		}
	}
	ncx := read(t, r, "OEBPS/toc.ncx")
	if strings.Count(ncx, "<navPoint") != 3 { // title page plus two chapters
		t.Errorf("wrong number of entries in toc.ncx:\n%s", ncx)
	}
}

func TestBookWithoutChaptersFails(t *testing.T) {
	b := &epub.Book{Metadata: epub.Metadata{Title: "Empty"}}
	if _, err := b.Bytes(); err == nil {
		t.Fatal("a book with no chapters must be an error")
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

// A book's wording follows its language, so a Russian book does not end up with
// an English table of contents.
func TestLabelsFollowLanguage(t *testing.T) {
	b := sample()
	b.Metadata.Language = "ru-RU"
	r := open(t, b)

	nav := read(t, r, "OEBPS/nav.xhtml")
	for _, want := range []string{"<h1>Оглавление</h1>", "<li><span>Том 1</span>"} {
		if !strings.Contains(nav, want) {
			t.Errorf("the contents are missing %q", want)
		}
	}
	if title := read(t, r, "OEBPS/text/title.xhtml"); !strings.Contains(title, "Аннотация") {
		t.Errorf("the title page did not switch to Russian wording")
	}
	if got := epub.LabelsFor("ru").Chapter; got != "Глава" {
		t.Errorf("LabelsFor(ru).Chapter = %q", got)
	}
	if got := epub.LabelsFor("de").TableOfContents; got != epub.DefaultLabels.TableOfContents {
		t.Errorf("an unknown language must fall back to the defaults, got %q", got)
	}
}

// Explicit labels win over the language.
func TestExplicitLabelsWin(t *testing.T) {
	b := sample()
	b.Metadata.Language = "ru"
	b.Labels = epub.Labels{TableOfContents: "Contents"}
	r := open(t, b)

	nav := read(t, r, "OEBPS/nav.xhtml")
	if !strings.Contains(nav, "<h1>Contents</h1>") {
		t.Errorf("explicit labels were ignored:\n%s", nav)
	}
	// Fields left empty still fall back to the English defaults, not to Russian.
	if !strings.Contains(nav, "<li><span>Volume 1</span>") {
		t.Errorf("an empty field should fall back to the defaults:\n%s", nav)
	}
}
