// Package epub собирает валидный EPUB 3 с оглавлением, обложкой и метаданными.
//
// Оглавление пишется дважды: nav.xhtml для EPUB 3 и toc.ncx для читалок,
// которые его не понимают. Главы с указанным томом группируются в оглавлении
// по томам, если томов больше одного.
package epub

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"fmt"
	"hash/crc32"
	"html"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Metadata — сведения о книге.
type Metadata struct {
	Title         string
	OriginalTitle string
	Language      string // по умолчанию "ru"
	Authors       []string
	Translators   []string
	Genres        []string
	Publisher     string
	Date          string // год издания оригинала
	Description   string // аннотация, простым текстом
	Source        string // ссылка на источник
}

// Chapter — глава книги. Body — фрагмент XHTML без обёрток html/body.
type Chapter struct {
	Volume string
	Number string
	Title  string
	Body   string
}

// Image — картинка внутри книги. Name — имя файла, например "img-1.jpg".
type Image struct {
	Name      string
	MediaType string
	Data      []byte
}

// Book — книга целиком.
type Book struct {
	Metadata Metadata
	Cover    *Image
	Chapters []Chapter
	Images   []Image
	// CSS подменяет оформление по умолчанию.
	CSS string
	// ID — уникальный идентификатор книги. Пустой означает «сгенерировать».
	ID string
	// Modified — метка времени в метаданных. Нулевая означает текущее время.
	Modified time.Time
}

// MediaType возвращает MIME-тип картинки по расширению файла.
func MediaType(ext string) string {
	switch strings.ToLower(strings.TrimPrefix(ext, ".")) {
	case "png":
		return "image/png"
	case "gif":
		return "image/gif"
	case "webp":
		return "image/webp"
	case "svg":
		return "image/svg+xml"
	default:
		return "image/jpeg"
	}
}

// WriteFile собирает книгу в файл.
func (b *Book) WriteFile(name string) error {
	if dir := filepath.Dir(name); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	f, err := os.Create(name)
	if err != nil {
		return err
	}
	if _, err := b.WriteTo(f); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// WriteTo собирает книгу в поток. Реализует io.WriterTo.
func (b *Book) WriteTo(w io.Writer) (int64, error) {
	if len(b.Chapters) == 0 {
		return 0, fmt.Errorf("epub: в книге нет ни одной главы")
	}

	cnt := &countingWriter{w: w}
	zw := zip.NewWriter(cnt)

	// mimetype обязан лежать первым и без сжатия. CreateRaw используется,
	// чтобы zip не добавил к записи дескриптор данных: валидаторы EPUB на это ругаются.
	if err := writeMimetype(zw); err != nil {
		return cnt.n, err
	}

	meta := b.Metadata
	if meta.Language == "" {
		meta.Language = "ru"
	}
	id := b.ID
	if id == "" {
		id = "urn:uuid:" + uuid()
	}
	modified := b.Modified
	if modified.IsZero() {
		modified = time.Now().UTC()
	}

	add := func(name, body string) error { return writeFile(zw, name, []byte(body)) }

	if err := add("META-INF/container.xml", containerXML); err != nil {
		return cnt.n, err
	}
	css := b.CSS
	if css == "" {
		css = DefaultCSS
	}
	if err := add("OEBPS/styles/main.css", css); err != nil {
		return cnt.n, err
	}

	var manifest, spine []string
	item := func(id, href, mediaType, extra string) {
		manifest = append(manifest, fmt.Sprintf(`<item id=%q href=%q media-type=%q%s/>`, id, href, mediaType, extra))
	}
	itemref := func(id string) { spine = append(spine, fmt.Sprintf(`<itemref idref=%q linear="yes"/>`, id)) }

	if b.Cover != nil {
		href := "images/" + b.Cover.Name
		if err := writeFile(zw, "OEBPS/"+href, b.Cover.Data); err != nil {
			return cnt.n, err
		}
		item("cover-image", href, mediaTypeOf(*b.Cover), ` properties="cover-image"`)
		if err := add("OEBPS/text/cover.xhtml", page("Обложка",
			`<div class="img cover"><img src="../`+href+`" alt="Обложка"/></div>`)); err != nil {
			return cnt.n, err
		}
		item("cover", "text/cover.xhtml", "application/xhtml+xml", "")
		itemref("cover")
	}

	if err := add("OEBPS/text/title.xhtml", titlePage(meta)); err != nil {
		return cnt.n, err
	}
	item("title", "text/title.xhtml", "application/xhtml+xml", "")
	itemref("title")

	type navItem struct{ id, href, title, volume string }
	nav := make([]navItem, 0, len(b.Chapters))
	for i, ch := range b.Chapters {
		id := fmt.Sprintf("ch%04d", i+1)
		href := "text/" + id + ".xhtml"
		title := ch.Title
		if title == "" {
			title = "Глава " + ch.Number
		}
		body := `<h1 class="chapter-title">` + esc(title) + "</h1>\n"
		if ch.Volume != "" {
			body += `<p class="chapter-meta">Том ` + esc(ch.Volume) + "</p>\n"
		}
		body += ch.Body
		if err := add("OEBPS/"+href, page(title, body)); err != nil {
			return cnt.n, err
		}
		item(id, href, "application/xhtml+xml", "")
		itemref(id)
		nav = append(nav, navItem{id, href, title, ch.Volume})
	}

	for i, img := range b.Images {
		href := "images/" + img.Name
		if err := writeFile(zw, "OEBPS/"+href, img.Data); err != nil {
			return cnt.n, err
		}
		item(fmt.Sprintf("img%04d", i+1), href, mediaTypeOf(img), "")
	}

	// Оглавление: по томам, если томов больше одного.
	var volumes []string
	seen := map[string]bool{}
	for _, n := range nav {
		if n.volume != "" && !seen[n.volume] {
			seen[n.volume] = true
			volumes = append(volumes, n.volume)
		}
	}
	var list strings.Builder
	if len(volumes) > 1 {
		for _, v := range volumes {
			list.WriteString("      <li><span>Том " + esc(v) + "</span>\n        <ol>\n")
			for _, n := range nav {
				if n.volume == v {
					list.WriteString(`          <li><a href="` + n.href + `">` + esc(n.title) + "</a></li>\n")
				}
			}
			list.WriteString("        </ol>\n      </li>\n")
		}
	} else {
		for _, n := range nav {
			list.WriteString(`      <li><a href="` + n.href + `">` + esc(n.title) + "</a></li>\n")
		}
	}
	if err := add("OEBPS/nav.xhtml", navDoc(meta.Title, nav[0].href, list.String())); err != nil {
		return cnt.n, err
	}
	item("nav", "nav.xhtml", "application/xhtml+xml", ` properties="nav"`)

	var points strings.Builder
	for i, n := range nav {
		fmt.Fprintf(&points, `    <navPoint id="np-%s" playOrder="%d">
      <navLabel><text>%s</text></navLabel>
      <content src="%s"/>
    </navPoint>
`, n.id, i+2, esc(n.title), n.href)
	}
	if err := add("OEBPS/toc.ncx", ncxDoc(id, meta.Title, points.String())); err != nil {
		return cnt.n, err
	}
	item("ncx", "toc.ncx", "application/x-dtbncx+xml", "")
	item("css", "styles/main.css", "text/css", "")

	if err := add("OEBPS/content.opf", opfDoc(id, meta, modified, b.Cover != nil, manifest, spine)); err != nil {
		return cnt.n, err
	}

	if err := zw.Close(); err != nil {
		return cnt.n, err
	}
	return cnt.n, nil
}

func mediaTypeOf(img Image) string {
	if img.MediaType != "" {
		return img.MediaType
	}
	return MediaType(filepath.Ext(img.Name))
}

func writeMimetype(zw *zip.Writer) error {
	const mimetype = "application/epub+zip"
	h := &zip.FileHeader{
		Name:               "mimetype",
		Method:             zip.Store,
		CRC32:              crc32.ChecksumIEEE([]byte(mimetype)),
		CompressedSize64:   uint64(len(mimetype)),
		UncompressedSize64: uint64(len(mimetype)),
	}
	w, err := zw.CreateRaw(h)
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, mimetype)
	return err
}

func writeFile(zw *zip.Writer, name string, data []byte) error {
	w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

func esc(s string) string { return html.EscapeString(s) }

// uuid собирает UUID версии 4 без внешних зависимостей.
func uuid() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand на практике не отказывает; если отказал — метка времени сгодится.
		return fmt.Sprintf("%016x-0000-4000-8000-000000000000", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// Bytes собирает книгу в память — удобно для тестов и отправки по сети.
func (b *Book) Bytes() ([]byte, error) {
	var buf bytes.Buffer
	if _, err := b.WriteTo(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
