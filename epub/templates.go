package epub

import (
	"fmt"
	"strings"
	"time"
)

// Labels are the words the builder writes into the book itself. The zero value
// falls back to DefaultLabels, so a book in another language can be produced
// without touching the rest of the package.
type Labels struct {
	TableOfContents string
	Annotation      string
	Author          string
	Translation     string
	Year            string
	Genres          string
	Source          string
	Cover           string
	// Chapter prefixes a chapter number in a heading, e.g. "Chapter 12".
	Chapter string
	// Volume prefixes a volume number, e.g. "Volume 3".
	Volume string
	// Start names the landmark pointing at the first chapter.
	Start         string
	UnknownAuthor string
}

// LabelsFor returns the wording for a language tag, falling back to
// DefaultLabels for anything unknown. Only the primary subtag matters, so
// "ru-RU" and "ru" give the same answer.
func LabelsFor(lang string) Labels {
	primary, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(lang)), "-")
	if l, ok := labelsByLanguage[primary]; ok {
		return l
	}
	return DefaultLabels
}

// labelsByLanguage holds the built-in wording. Callers with another language set
// Book.Labels themselves.
var labelsByLanguage = map[string]Labels{
	"ru": {
		TableOfContents: "Оглавление",
		Annotation:      "Аннотация",
		Author:          "Автор",
		Translation:     "Перевод",
		Year:            "Год",
		Genres:          "Жанры",
		Source:          "Источник",
		Cover:           "Обложка",
		Chapter:         "Глава",
		Volume:          "Том",
		Start:           "Начало",
		UnknownAuthor:   "Неизвестный автор",
	},
}

// DefaultLabels are the English defaults.
var DefaultLabels = Labels{
	TableOfContents: "Table of contents",
	Annotation:      "Annotation",
	Author:          "Author",
	Translation:     "Translation",
	Year:            "Year",
	Genres:          "Genres",
	Source:          "Source",
	Cover:           "Cover",
	Chapter:         "Chapter",
	Volume:          "Volume",
	Start:           "Start",
	UnknownAuthor:   "Unknown author",
}

func (l Labels) withDefaults() Labels {
	d := DefaultLabels
	for _, f := range []struct {
		dst *string
		def string
	}{
		{&l.TableOfContents, d.TableOfContents},
		{&l.Annotation, d.Annotation},
		{&l.Author, d.Author},
		{&l.Translation, d.Translation},
		{&l.Year, d.Year},
		{&l.Genres, d.Genres},
		{&l.Source, d.Source},
		{&l.Cover, d.Cover},
		{&l.Chapter, d.Chapter},
		{&l.Volume, d.Volume},
		{&l.Start, d.Start},
		{&l.UnknownAuthor, d.UnknownAuthor},
	} {
		if *f.dst == "" {
			*f.dst = f.def
		}
	}
	return l
}

// DefaultCSS is the built-in typography: paragraph indent, justified text,
// hyphenation, tidy block quotes and translator's notes.
const DefaultCSS = `@charset "utf-8";

body { margin: 0 5%; line-height: 1.5; text-align: justify; hyphens: auto; -webkit-hyphens: auto; }

h1, h2, h3 { text-align: left; line-height: 1.25; page-break-after: avoid; break-after: avoid; }
h1.chapter-title { font-size: 1.35em; margin: 1em 0 0.2em; }

p { margin: 0; text-indent: 1.2em; }
p + p { margin-top: 0.15em; }
p.empty { text-indent: 0; margin: 0.6em 0; }
p.note { text-indent: 0; margin: 0.6em 0; font-size: 0.9em; color: #555; }
p.chapter-meta { margin: 0 0 1.6em; font-size: 0.85em; color: #666; text-align: left; }

blockquote { margin: 0.8em 1.5em; font-style: italic; }
blockquote p { text-indent: 0; }

hr { border: 0; border-top: 1px solid currentColor; opacity: 0.35; margin: 1.4em 20%; }

div.img { text-align: center; text-indent: 0; margin: 1em 0; page-break-inside: avoid; break-inside: avoid; }
div.img img { max-width: 100%; max-height: 100%; }
div.cover { margin: 0; }

ul, ol { margin: 0.6em 0 0.6em 1.4em; padding: 0; }
li { text-indent: 0; }

table { border-collapse: collapse; margin: 1em auto; }
td, th { border: 1px solid #999; padding: 0.3em 0.5em; text-indent: 0; }

.title-page { text-align: center; margin-top: 15%; }
.title-page h1 { font-size: 1.8em; text-align: center; margin-bottom: 0.2em; }
.title-page .orig { font-size: 1em; color: #666; margin-bottom: 2em; text-indent: 0; }
.title-page .meta { text-indent: 0; margin: 0.35em 0; }

.annotation { margin-top: 2.5em; text-align: left; }
.annotation h2 { font-size: 1.05em; }
`

const containerXML = `<?xml version="1.0" encoding="utf-8"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>
`

// page wraps a fragment into a standalone XHTML document.
func page(title, lang, body string) string {
	return `<?xml version="1.0" encoding="utf-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops" xml:lang="` + esc(lang) + `" lang="` + esc(lang) + `">
<head>
  <meta charset="utf-8"/>
  <title>` + esc(title) + `</title>
  <link rel="stylesheet" type="text/css" href="../styles/main.css"/>
</head>
<body>
` + body + `
</body>
</html>
`
}

func titlePage(m Metadata, l Labels) string {
	var b strings.Builder
	b.WriteString(`<div class="title-page">` + "\n  <h1>" + esc(m.Title) + "</h1>\n")
	if m.OriginalTitle != "" && m.OriginalTitle != m.Title {
		b.WriteString(`  <p class="orig">` + esc(m.OriginalTitle) + "</p>\n")
	}
	row := func(label, value string) {
		if value != "" {
			b.WriteString(`  <p class="meta">` + esc(label) + ": " + esc(value) + "</p>\n")
		}
	}
	row(l.Author, strings.Join(m.Authors, ", "))
	row(l.Translation, strings.Join(m.Translators, ", "))
	row(l.Year, m.Date)
	row(l.Genres, strings.Join(m.Genres, ", "))
	row(l.Source, m.Source)
	b.WriteString("</div>\n")

	if d := strings.TrimSpace(m.Description); d != "" {
		b.WriteString(`<div class="annotation"><h2>` + esc(l.Annotation) + "</h2>\n")
		for _, p := range strings.Split(d, "\n\n") {
			if p = strings.TrimSpace(p); p != "" {
				b.WriteString("<p>" + esc(p) + "</p>\n")
			}
		}
		b.WriteString("</div>\n")
	}
	return page(m.Title, m.Language, b.String())
}

func navDoc(title, lang, first, list string, l Labels) string {
	return `<?xml version="1.0" encoding="utf-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops" xml:lang="` + esc(lang) + `" lang="` + esc(lang) + `">
<head>
  <meta charset="utf-8"/>
  <title>` + esc(l.TableOfContents) + `</title>
  <link rel="stylesheet" type="text/css" href="styles/main.css"/>
</head>
<body>
  <nav epub:type="toc" id="toc">
    <h1>` + esc(l.TableOfContents) + `</h1>
    <ol>
      <li><a href="text/title.xhtml">` + esc(title) + `</a></li>
` + list + `    </ol>
  </nav>
  <nav epub:type="landmarks" hidden="hidden">
    <ol>
      <li><a epub:type="bodymatter" href="` + first + `">` + esc(l.Start) + `</a></li>
    </ol>
  </nav>
</body>
</html>
`
}

func ncxDoc(id, title, points string) string {
	return `<?xml version="1.0" encoding="utf-8"?>
<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/" version="2005-1">
  <head>
    <meta name="dtb:uid" content="` + esc(id) + `"/>
    <meta name="dtb:depth" content="1"/>
    <meta name="dtb:totalPageCount" content="0"/>
    <meta name="dtb:maxPageNumber" content="0"/>
  </head>
  <docTitle><text>` + esc(title) + `</text></docTitle>
  <navMap>
    <navPoint id="np-title" playOrder="1">
      <navLabel><text>` + esc(title) + `</text></navLabel>
      <content src="text/title.xhtml"/>
    </navPoint>
` + points + `  </navMap>
</ncx>
`
}

func opfDoc(id string, m Metadata, l Labels, modified time.Time, hasCover bool, manifest, spine []string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="bookid" xml:lang="` + esc(m.Language) + `">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:identifier id="bookid">` + esc(id) + `</dc:identifier>
    <dc:title>` + esc(m.Title) + `</dc:title>
    <dc:language>` + esc(m.Language) + `</dc:language>
`)
	if len(m.Authors) == 0 {
		m.Authors = []string{l.UnknownAuthor}
	}
	for i, a := range m.Authors {
		fmt.Fprintf(&b, "    <dc:creator id=\"creator-%d\">%s</dc:creator>\n", i, esc(a))
	}
	for i, t := range m.Translators {
		fmt.Fprintf(&b, "    <dc:contributor id=\"contrib-%d\">%s</dc:contributor>\n", i, esc(t))
	}
	for _, g := range m.Genres {
		b.WriteString("    <dc:subject>" + esc(g) + "</dc:subject>\n")
	}
	if m.Publisher != "" {
		b.WriteString("    <dc:publisher>" + esc(m.Publisher) + "</dc:publisher>\n")
	}
	if d := strings.TrimSpace(m.Description); d != "" {
		if len([]rune(d)) > 4000 {
			d = string([]rune(d)[:4000])
		}
		b.WriteString("    <dc:description>" + esc(d) + "</dc:description>\n")
	}
	if m.Source != "" {
		b.WriteString("    <dc:source>" + esc(m.Source) + "</dc:source>\n")
	}
	if m.Date != "" {
		b.WriteString("    <dc:date>" + esc(m.Date) + "</dc:date>\n")
	}
	b.WriteString("    <meta property=\"dcterms:modified\">" + modified.UTC().Format("2006-01-02T15:04:05Z") + "</meta>\n")
	if hasCover {
		b.WriteString("    <meta name=\"cover\" content=\"cover-image\"/>\n")
	}
	b.WriteString("  </metadata>\n  <manifest>\n")
	for _, it := range manifest {
		b.WriteString("    " + it + "\n")
	}
	b.WriteString("  </manifest>\n  <spine toc=\"ncx\">\n")
	for _, it := range spine {
		b.WriteString("    " + it + "\n")
	}
	b.WriteString("  </spine>\n</package>\n")
	return b.String()
}
