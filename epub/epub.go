// Package epub assembles a valid EPUB 3 with a table of contents, a cover and
// metadata.
//
// The table of contents is written twice: nav.xhtml for EPUB 3 and toc.ncx for
// readers that do not understand it. When a book spans more than one volume,
// the contents are grouped by volume.
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

// Metadata describes the book.
type Metadata struct {
	Title         string
	OriginalTitle string
	Language      string // defaults to "en"
	Authors       []string
	Translators   []string
	Genres        []string
	Publisher     string
	Date          string // year the original was published
	Description   string // blurb, as plain text
	Source        string // link to where it came from
}

// Chapter is one chapter. Body is an XHTML fragment without html/body wrappers.
type Chapter struct {
	Volume string
	Number string
	Title  string
	Body   string
}

// Image is a picture inside the book. Name is its file name, e.g. "img-1.jpg".
type Image struct {
	Name      string
	MediaType string
	Data      []byte
}

// Book is a whole book.
type Book struct {
	Metadata Metadata
	Cover    *Image
	Chapters []Chapter
	Images   []Image
	// CSS replaces the built-in styling.
	CSS string
	// Labels are the words written into the book itself; empty fields fall back
	// to DefaultLabels.
	Labels Labels
	// ID is the book's unique identifier. Empty means "generate one".
	ID string
	// Modified is the timestamp in the metadata. Zero means "now".
	Modified time.Time
}

// MediaType returns a picture's MIME type from its file extension.
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

// WriteFile assembles the book into a file.
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

// WriteTo assembles the book into a stream. It implements io.WriterTo.
func (b *Book) WriteTo(w io.Writer) (int64, error) {
	if len(b.Chapters) == 0 {
		return 0, fmt.Errorf("epub: the book has no chapters")
	}

	cnt := &countingWriter{w: w}
	zw := zip.NewWriter(cnt)

	// mimetype must come first and be stored uncompressed. CreateRaw keeps the
	// zip writer from adding a data descriptor, which EPUB validators complain about.
	if err := writeMimetype(zw); err != nil {
		return cnt.n, err
	}

	meta := b.Metadata
	if meta.Language == "" {
		meta.Language = "en"
	}
	labels := b.Labels.withDefaults()
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
		if err := add("OEBPS/text/cover.xhtml", page(labels.Cover, meta.Language,
			`<div class="img cover"><img src="../`+href+`" alt="`+esc(labels.Cover)+`"/></div>`)); err != nil {
			return cnt.n, err
		}
		item("cover", "text/cover.xhtml", "application/xhtml+xml", "")
		itemref("cover")
	}

	if err := add("OEBPS/text/title.xhtml", titlePage(meta, labels)); err != nil {
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
			title = ch.Number
		}
		body := `<h1 class="chapter-title">` + esc(title) + "</h1>\n"
		if ch.Volume != "" {
			body += `<p class="chapter-meta">` + esc(labels.Volume+" "+ch.Volume) + "</p>\n"
		}
		body += ch.Body
		if err := add("OEBPS/"+href, page(title, meta.Language, body)); err != nil {
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

	// Contents: grouped by volume when there is more than one.
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
			list.WriteString("      <li><span>" + esc(labels.Volume+" "+v) + "</span>\n        <ol>\n")
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
	if err := add("OEBPS/nav.xhtml", navDoc(meta.Title, meta.Language, nav[0].href, list.String(), labels)); err != nil {
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

	if err := add("OEBPS/content.opf", opfDoc(id, meta, labels, modified, b.Cover != nil, manifest, spine)); err != nil {
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

// uuid builds a version 4 UUID without pulling in a dependency.
func uuid() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail in practice; if it does, a timestamp will do.
		return fmt.Sprintf("%016x-0000-4000-8000-000000000000", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// Bytes assembles the book in memory, which is handy for tests and for sending it somewhere.
func (b *Book) Bytes() ([]byte, error) {
	var buf bytes.Buffer
	if _, err := b.WriteTo(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
