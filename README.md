# novelkit

Download books from reader sites and assemble them into EPUB: a Go library and a
command-line tool built on top of it.

The core knows nothing about any particular site. A new site is plugged in by
implementing one interface; everything else — caching, resumable downloads, book
assembly, image compression — is already written and behaves the same for every
source. So far ranobelib.me is supported.

```sh
go install github.com/fess932/novelkit/cmd/novelkit@latest   # the tool
go get github.com/fess932/novelkit                           # the library
```

## Dependencies

| What | Why | Required |
| --- | --- | --- |
| Go 1.24+ | building | yes |
| `golang.org/x/net` | parsing chapter HTML | yes |
| `golang.org/x/image` | scaling illustrations | yes |
| `github.com/gen2brain/jpegli` | JPEG encoding (libjxl in WASM) | yes |

No external programs at all: no ImageMagick, no ffmpeg, no cgo. Compression
happens entirely in-process.

# The tool

The easiest way is to run it with no arguments and let it ask:

```
What are we doing?
 ❯  1. Download a new book             — by link or title
    2. Continue a download             — unfinished: 1
    3. Assemble an EPUB from the cache — books cached: 2
    4. Show what is cached
```

Move with the arrow keys (or `j`/`k`, or by typing an entry number), Enter
confirms, `q` quits. Then come the book, the translation, the chapter range and
a question about shrinking illustrations.

The same things are available as flags:

```sh
# by link or slug — the translation and range are chosen interactively
novelkit https://ranobelib.me/ru/book/14841--beginning-after-the-end-novel

# by title (search, then pick from a list)
novelkit "Beginning After The End"

# see which translations exist
novelkit 14841--beginning-after-the-end-novel --list-editions

# no questions: a specific translation, range and compression
novelkit 14841--beginning-after-the-end-novel --edition 9824 --from 1 --to 100 --compress --yes
```

### Translations

A book usually has several translations — the tabs above the chapter list.
`--list-editions` prints them the way the site shows them:

```
Beginning After The End — translations: 4
  id=9824  550 chapters  Silent Step & Эрл Грей («Ничоси 2») [Theunt, AtLas, …]
  id=9823  301 chapters  Kyu Team & Rulate Project & FiuTeam («Ничоси 1») […]
  id=11722  17 chapters  Aniker Team & Lipov Team («Webfandom») [Andrey Lipov]
  id=26435  no chapters  Альтернативный перевод
```

In parentheses is the site's internal name, in brackets the people who posted the
chapters. A translation with no chapters is listed but cannot be chosen. Pick one
by id (`--edition 9824`) or by team name (`--edition-name "Эрл Грей"`).

### Titles that need an account

Some titles are invisible to anonymous requests: the API answers 404 for them,
exactly as it does for a book that never existed. If a link opens in a signed-in
browser but the tool reports "not found", that is what happened — and the error
says so.

The site authorises the API with a bearer token, which a signed-in browser keeps
in an object like this:

```json
{"token_type": "Bearer", "expires_in": 2678400, "access_token": "eyJ0eXAi…", "refresh_token": "def50200…"}
```

The one that matters is `access_token`; `refresh_token` is not accepted. Pasting
the whole object works too — the access token is picked out of it:

```sh
export RANOBELIB_TOKEN='eyJ0eXAi…'
novelkit https://ranobelib.me/ru/book/230300--...
```

`expires_in` is 31 days exactly, so the token stops working after a month; a 401
then says as much and a fresh one has to be copied.

This is a credential of an existing session — nothing here signs in for you, and
no password is ever involved. Keep it in the environment rather than in shell
history, and treat it as carefully as a password.

### Stopping and resuming

A download stops at the first unrecoverable error, keeping what it already has:

```sh
novelkit --resume                  # the most recent job
novelkit --resume .novelkit/ranobelib-14841--...--9824
novelkit --list-jobs               # what is unfinished
```

The cache holds the site's raw responses, so rebuilding a book costs no requests
at all (`--build-only`), and widening the range only fetches the new chapters.

### Chapter range

`--from 250` runs from the 250th to the end, `--to 100` takes the first hundred,
both together take a slice. The interactive prompt accepts the same: `250`,
`1-100`, or Enter for everything. These are positions within the chosen
translation, not the chapter numbers printed on the site.

### Options

| Option | Meaning |
| --- | --- |
| `--edition <id>` / `--edition-name <s>` | which translation to download |
| `--list-editions` | print the translations and exit |
| `--from <n>` / `--to <n>` | chapter range |
| `--out <file>` | path to the .epub |
| `--work-dir <dir>` | cache directory (default `.novelkit`) |
| `--delay` / `--jitter` / `--retries` | request pacing and retry count |
| `--no-images` | skip illustrations |
| `--compress` + `--max-image` / `--quality` | shrink illustrations (1200 / 82) |
| `--build-only` | assemble from the cache, download nothing |
| `--refresh-meta` | refresh the blurb, author and genres |
| `--resume [dir]` / `--list-jobs` | resuming and the job list |
| `--token <token>` | token of a signed-in session; some titles are invisible without it |
| `--yes` | no questions |

# The library

| Package | Responsible for | Site-aware |
| --- | --- | --- |
| `novel` | shared types and the `Source` interface | no |
| `markup` | markup to XHTML: HTML and ProseMirror | no |
| `epub` | EPUB 3 assembly | no |
| `job` | job cache, resuming, assembling from cache | no |
| `imagex` | scaling and re-compressing pictures | no |
| `sources/ranobelib` | ranobelib.me | yes |
| `cmd/novelkit` | the tool | — |

The layers are independent: take only the EPUB writer, only the markup parser, or
all of it. The library knows nothing about the tool — every prompt and every line
of output lives in `cmd/novelkit`.

```go
ctx := context.Background()

var registry novel.Registry
registry.Register(ranobelib.NewSource())

src, bookID, err := registry.Resolve("https://ranobelib.me/ru/book/14841--beginning-after-the-end-novel")

book, err := src.Book(ctx, bookID)
for _, e := range book.Editions {
    fmt.Printf("%s: %s — %d chapters\n", e.ID, e.Label(), e.Chapters)
}

store, err := job.OpenStore(".novelkit")
j, err := store.Plan(ctx, src, job.Request{
    BookID: bookID, EditionID: "9824", From: 1, To: 100, WithImages: true,
})

// Stops at the first error; calling it again resumes from the same place
err = j.Download(ctx, src, job.DownloadOptions{
    OnChapter: func(e job.Event) {
        fmt.Printf("%d/%d %s (about %v left)\n", e.Progress.Done, e.Progress.Total, e.Chapter.Title(), e.ETA)
    },
})
var chErr *job.ChapterError
if errors.As(err, &chErr) {
    fmt.Printf("stopped at chapter %s: %v\n", chErr.Chapter.Number, chErr.Err)
}

// Assembly never touches the network
opt, _ := imagex.NewResizer(filepath.Join(j.Dir(), "min"), 1200, 82)
res, err := j.BuildFile(ctx, src, "book.epub", job.BuildOptions{Optimizer: opt})
```

## Adding a site

Implement `novel.Source` — nine methods:

```go
type Source interface {
    ID() string                                                    // "ranobelib"
    Supports(rawURL string) bool
    ParseRef(rawURL string) (bookID string, ok bool)
    Search(ctx, query string) ([]Book, error)                      // may return ErrUnsupported
    Book(ctx, bookID string) (*Book, error)                        // details plus translations
    Chapters(ctx, bookID, editionID string) ([]ChapterInfo, error)
    Chapter(ctx, bookID, editionID string, ci ChapterInfo) (*Chapter, error)
    DecodeChapter(raw []byte) (*Chapter, error)                    // a chapter from the cache
    Fetch(ctx, rawURL string) ([]byte, string, error)              // pictures and covers
}
```

You will not have to write a markup parser: `markup.HTML` covers sites that serve
chapters as markup and `markup.ProseMirror` covers editor documents. Both
implement `novel.Content`, and `markup.Auto` picks between them when a site sends
one shape sometimes and the other the rest of the time.

A link is routed by shape, not by a table of known URL paths: `Supports` claims
the site and `ParseRef` pulls the book identifier out of whatever path the site
happens to use today. `Registry.Resolve` reports the two failures separately —
`ErrUnsupported` when no source claims the link at all, `ErrBadReference` when one
did but the address carries no book identifier — because the person reading the
error has to do something different in each case.

`Chapter.Raw` is the site's own response; the cache stores exactly that and
`DecodeChapter` turns it back into a chapter, so fixing a parser never means
downloading anything again.

`ranobelib.WithToken` does the same for library users: the token rides along with
every request, including file downloads.

A source paces its own requests: the core does not do it, and sites cut off
clients that hammer them. `sources/ranobelib` ships a client that does this —
a serial queue, a pause with jitter, and proper handling of 429.

## Downloading and resuming

A job is a directory: `job.json`, raw chapter responses under `chapters/` and
pictures under `assets/`. A chapter is marked done right after it is written, so
an interruption never costs the chapters before it.

- `Download` returns a `*job.ChapterError` — it says which chapter stopped it and why;
- calling it again fetches only what is missing;
- widening the range in `Plan` leaves downloaded chapters alone;
- `Build` assembles from the cache without touching the network; chapters that are
  not cached are skipped and counted in `BuildResult.Missing`;
- a job remembers its source and refuses to be driven by another one.

## Compressing illustrations

```go
opt, err := imagex.NewResizer(dir, 1200, 82) // cap on the longer side, jpeg quality
```

Originals are never touched: results go to a separate directory, so a book can
always be rebuilt with different settings or with no compression at all
(`imagex.Passthrough{}`).

JPEG is encoded with jpegli — the libjxl encoder compiled to WASM (pure Go, no
cgo). Measured on real illustrations at quality 82 and a 1200 px cap:

| encoder | size | DSSIM ↓ | PSNR ↑ |
| --- | --- | --- | --- |
| `image/jpeg` from the standard library | 45% of the original | 0.0198 | 36.8 dB |
| jpegli | **39%** | **0.0171** | **37.1 dB** |

That is 14% smaller *and* closer to the original, rather than a trade. There is
only one encoder: if it fails, `Optimize` returns an error and the picture goes
into the book untouched — no silent substitution.

A picture with transparency stays PNG (as JPEG its background would turn black),
animations are left alone, and when the compressed version comes out heavier than
the original, the original wins.

## Assembling an EPUB on its own

`epub.Book` knows nothing about sites or caches; feed it any chapters you like.

```go
book := &epub.Book{
    Metadata: epub.Metadata{Title: "Book", Authors: []string{"Author"}},
    Chapters: []epub.Chapter{{Volume: "1", Number: "1", Title: "Chapter 1", Body: "<p>Text</p>"}},
}
err := book.WriteFile("book.epub")   // or book.WriteTo(w), book.Bytes()
```

`mimetype` is written as the first entry, stored and without a data descriptor;
the table of contents is produced in both formats (`nav.xhtml` and `toc.ncx`) and
grouped by volume when there is more than one. Inside are a title page with the
blurb, the cover, one file per chapter, and print-like typography: paragraph
indents, justified text, hyphenation.

The words the builder writes into the book — "Table of contents", "Volume",
"Annotation" and so on — follow the book's language, which its source reports in
`novel.Book.Language`. ranobelib.me serves Russian translations, so its books come
out with Оглавление, Том and Глава without anyone asking.

`epub.LabelsFor(lang)` exposes the same lookup, and setting `Book.Labels`
explicitly overrides it:

```go
book.Labels = epub.Labels{TableOfContents: "Contents", Volume: "Book"}
```

## Tests

```sh
go test ./...     # no network: markup, EPUB, a full cycle against a fake source
go test -race ./...

RANOBELIB_LIVE=1 go test -run TestLive -v ./sources/ranobelib/   # live run: two chapters
```

The `job` tests run against a fake source that never mentions ranobelib, which
doubles as proof that the interface is enough for a new site.
