# ranobelib — библиотека на Go

Скачивание книг с сайтов-читалок и сборка EPUB. Библиотека, не приложение:
интерфейс пользователя, выбор перевода и показ прогресса — на стороне того, кто её встраивает.

Ядро не знает ни про один сайт. Новый сайт подключается реализацией одного интерфейса,
всё остальное — кэш, докачка после обрыва, сборка книги, сжатие картинок — уже написано
и работает с любым источником одинаково.

```go
import "github.com/fess932/novelkit/novel"
```

## Зависимости

| Что | Зачем | Обязательно |
| --- | --- | --- |
| Go 1.24+ | сама библиотека | да |
| `golang.org/x/net` | разбор HTML-разметки глав | да |
| `golang.org/x/image` | масштабирование иллюстраций | да |

Внешних программ не нужно вообще: ни ImageMagick, ни ffmpeg, ни cgo.
Сжатие целиком внутри процесса — стандартные кодеки плюс ресемплер из `x/image/draw`.

## Слои

| Пакет | Отвечает за | Знает про сайты |
| --- | --- | --- |
| `novel` | общие типы и интерфейс `Source` | нет |
| `markup` | разметка → XHTML: HTML и ProseMirror | нет |
| `epub` | сборка EPUB 3 | нет |
| `job` | кэш заданий, докачка, сборка из кэша | нет |
| `imagex` | уменьшение и пережатие картинок | нет |
| `sources/ranobelib` | ranobelib.me | да |

Слои независимы: можно взять только сборщик EPUB, только разбор разметки или всё вместе.

## Пример

```go
ctx := context.Background()

var registry novel.Registry
registry.Register(ranobelib.NewSource())

src, bookID, err := registry.Resolve("https://ranobelib.me/ru/book/14841--beginning-after-the-end-novel")

book, err := src.Book(ctx, bookID)
for _, e := range book.Editions {
    fmt.Printf("%s: %s — %d гл.\n", e.ID, e.Label(), e.Chapters)
}
// 9824: Silent Step & Эрл Грей — 550 гл.
// 9823: Kyu Team & Rulate Project & FiuTeam — 301 гл.
// 26435: Альтернативный перевод — 0 гл.

store, err := job.OpenStore(".rlib")
j, err := store.Plan(ctx, src, job.Request{
    BookID: bookID, EditionID: "9824", From: 1, To: 100, WithImages: true,
})

// Останавливается на первой ошибке; повторный вызов продолжает с того же места
err = j.Download(ctx, src, job.DownloadOptions{
    OnChapter: func(e job.Event) {
        fmt.Printf("%d/%d %s (осталось ~%v)\n", e.Progress.Done, e.Progress.Total, e.Chapter.Title(), e.ETA)
    },
})
var chErr *job.ChapterError
if errors.As(err, &chErr) {
    fmt.Printf("остановились на главе %s: %v\n", chErr.Chapter.Number, chErr.Err)
}

// Сборка в сеть не ходит
opt, _ := imagex.NewResizer(filepath.Join(j.Dir(), "min"), 1200, 82)
res, err := j.BuildFile(ctx, src, "книга.epub", job.BuildOptions{Optimizer: opt})
```

## Как подключить новый сайт

Реализовать `novel.Source` — девять методов:

```go
type Source interface {
    ID() string                                                    // "ranobelib"
    Supports(rawURL string) bool
    ParseRef(rawURL string) (bookID string, ok bool)
    Search(ctx, query string) ([]Book, error)                      // можно вернуть ErrUnsupported
    Book(ctx, bookID string) (*Book, error)                        // карточка + список переводов
    Chapters(ctx, bookID, editionID string) ([]ChapterInfo, error)
    Chapter(ctx, bookID, editionID string, ci ChapterInfo) (*Chapter, error)
    DecodeChapter(raw []byte) (*Chapter, error)                    // глава из кэша
    Fetch(ctx, rawURL string) ([]byte, string, error)              // картинки и обложка
}
```

Разбор разметки писать не нужно: в `markup` уже есть `markup.HTML` для сайтов, отдающих
главу разметкой, и `markup.ProseMirror` для документов редактора. Оба реализуют
`novel.Content`, а `markup.Auto` выбирает подходящий сам, если сайт отдаёт то одно, то другое.

`Chapter.Raw` — сырой ответ сайта; кэш хранит именно его, а обратно превращает `DecodeChapter`.
Поэтому починка разбора не требует перекачивать уже скачанное.

Темп запросов источник держит сам: ядро за него этого не делает, а сайты за частые
обращения закрывают доступ. В `sources/ranobelib` для этого есть готовый клиент с
последовательной очередью, паузой со случайным разбросом и отработкой 429.

## Загрузка и докачка

Задание — это каталог: `job.json`, сырые ответы по главам в `chapters/`, картинки в `assets/`.
Глава помечается выполненной сразу после записи на диск, поэтому обрыв не теряет предыдущие.

- `Download` возвращает `*job.ChapterError` — видно, на какой главе всё встало и почему;
- повторный вызов докачивает только недостающее;
- расширение диапазона в `Plan` не трогает уже скачанное;
- `Build` собирает книгу из кэша и в сеть не ходит; недокачанные главы пропускаются
  и считаются в `BuildResult.Missing`;
- задание помнит свой источник и не даст скачивать себя чужим.

## Сжатие иллюстраций

```go
opt, err := imagex.NewResizer(dir, 1200, 82) // предел по большей стороне, качество jpeg
```

Оригиналы не трогаются: результат пишется в отдельный каталог, поэтому книгу всегда можно
пересобрать с другими настройками или вовсе без сжатия (`imagex.Passthrough{}`).

На реальной книге — 318 иллюстраций, 248 МБ — получается 24.4 МБ за 25 секунд.
Для сравнения, ImageMagick на тех же настройках даёт 24.0 МБ: разница 1.7%, ради которой
внешнюю программу тянуть незачем.

Картинка с прозрачностью остаётся PNG (в JPEG у неё почернел бы фон), анимация не трогается,
а если пережатая версия вышла тяжелее исходной — берётся исходная.

## Сборка EPUB отдельно

`epub.Book` не знает ни про сайты, ни про кэш: ему можно скормить любые главы.

```go
book := &epub.Book{
    Metadata: epub.Metadata{Title: "Книга", Authors: []string{"Автор"}},
    Chapters: []epub.Chapter{{Volume: "1", Number: "1", Title: "Глава 1", Body: "<p>Текст</p>"}},
}
err := book.WriteFile("книга.epub")   // или book.WriteTo(w), book.Bytes()
```

`mimetype` пишется первой записью без сжатия и без дескриптора данных, оглавление —
в двух форматах (`nav.xhtml` и `toc.ncx`), тома группируются, если их больше одного.

## Отличия от версии на JS

Библиотека писалась заново, а не переводилась построчно, поэтому:

- **формат кэша свой** — задания JS-версии (`.rlib`) не читаются, они несовместимы;
- **интерфейса пользователя нет** — ни меню, ни флагов; всё это строит вызывающий;
- **строгий разбор JSON**: там, где JS молча проглатывал неожиданный тип поля
  (и однажды положил в аннотацию `[object Object]`), Go вернёт ошибку. Поля, которые сайт
  отдаёт в разных формах — содержимое главы и аннотация — хранятся сырыми и разбираются явно;
- **сжатие без внешних программ**, тогда как JS-версия зовёт ImageMagick.

## Тесты

```sh
go test ./...                 # без сети: разметка, EPUB, полный цикл на поддельном источнике
go test -race ./...
RANOBELIB_LIVE=1 go test -run TestLive -v ./sources/ranobelib/   # живой прогон: две главы
```

Тесты пакета `job` написаны на поддельном источнике, в котором нет ни строчки про ranobelib —
заодно это проверка того, что интерфейса хватает для нового сайта.
