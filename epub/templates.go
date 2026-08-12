package epub

import (
	"fmt"
	"strings"
	"time"
)

// DefaultCSS — типографика по умолчанию: абзацный отступ, выключка по ширине,
// переносы, аккуратные цитаты и примечания переводчика.
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

// page заворачивает фрагмент в самостоятельный XHTML-документ.
func page(title, body string) string {
	return `<?xml version="1.0" encoding="utf-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops" xml:lang="ru" lang="ru">
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

func titlePage(m Metadata) string {
	var b strings.Builder
	b.WriteString(`<div class="title-page">` + "\n  <h1>" + esc(m.Title) + "</h1>\n")
	if m.OriginalTitle != "" && m.OriginalTitle != m.Title {
		b.WriteString(`  <p class="orig">` + esc(m.OriginalTitle) + "</p>\n")
	}
	row := func(label string, value string) {
		if value != "" {
			b.WriteString(`  <p class="meta">` + esc(label) + ": " + esc(value) + "</p>\n")
		}
	}
	row("Автор", strings.Join(m.Authors, ", "))
	row("Перевод", strings.Join(m.Translators, ", "))
	row("Год", m.Date)
	row("Жанры", strings.Join(m.Genres, ", "))
	row("Источник", m.Source)
	b.WriteString("</div>\n")

	if d := strings.TrimSpace(m.Description); d != "" {
		b.WriteString(`<div class="annotation"><h2>Аннотация</h2>` + "\n")
		for _, p := range strings.Split(d, "\n\n") {
			if p = strings.TrimSpace(p); p != "" {
				b.WriteString("<p>" + esc(p) + "</p>\n")
			}
		}
		b.WriteString("</div>\n")
	}
	return page(m.Title, b.String())
}

func navDoc(title, first, list string) string {
	return `<?xml version="1.0" encoding="utf-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops" xml:lang="ru" lang="ru">
<head>
  <meta charset="utf-8"/>
  <title>Оглавление</title>
  <link rel="stylesheet" type="text/css" href="styles/main.css"/>
</head>
<body>
  <nav epub:type="toc" id="toc">
    <h1>Оглавление</h1>
    <ol>
      <li><a href="text/title.xhtml">` + esc(title) + `</a></li>
` + list + `    </ol>
  </nav>
  <nav epub:type="landmarks" hidden="hidden">
    <ol>
      <li><a epub:type="bodymatter" href="` + first + `">Начало</a></li>
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

func opfDoc(id string, m Metadata, modified time.Time, hasCover bool, manifest, spine []string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="bookid" xml:lang="` + esc(m.Language) + `">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:identifier id="bookid">` + esc(id) + `</dc:identifier>
    <dc:title>` + esc(m.Title) + `</dc:title>
    <dc:language>` + esc(m.Language) + `</dc:language>
`)
	if len(m.Authors) == 0 {
		m.Authors = []string{"Неизвестный автор"}
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
