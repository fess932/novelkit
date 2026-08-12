# ranobelib — библиотека на Go

Скачивание книг с ranobelib.me и сборка EPUB. Библиотека, не приложение:
интерфейс пользователя, выбор ветки перевода и показ прогресса — на стороне того, кто её встраивает.

```go
import "github.com/fess932/ranobelib"
```

## Зависимости

| Что | Зачем | Обязательно |
| --- | --- | --- |
| Go 1.24+ | сама библиотека | да |
| `golang.org/x/net` | разбор HTML-разметки глав | да |
| ImageMagick (`magick`) | пакет `imagex`, сжатие иллюстраций | нет |

## Пакеты

| Пакет | Отвечает за |
| --- | --- |
| `ranobelib` | клиент API: поиск, карточка книги, главы, ветки перевода, темп запросов |
| `ranobelib/content` | разбор содержимого главы в XHTML и в простой текст |
| `ranobelib/epub` | сборка EPUB 3 с оглавлением, обложкой и метаданными |
| `ranobelib/job` | кэш заданий на диске: докачка после обрыва и сборка книги |
| `ranobelib/imagex` | сжатие иллюстраций через ImageMagick |

Слои независимы: можно взять только клиент, только сборщик EPUB или всё вместе.

## Пример

```go
ctx := context.Background()
c := ranobelib.New()

// 1. Что за книга и чьи переводы у неё есть
manga, err := c.Manga(ctx, "14841--beginning-after-the-end-novel")
chapters, err := c.Chapters(ctx, manga.SlugURL)
cards, err := c.Branches(ctx, manga.ID)

for _, b := range ranobelib.CollectBranches(chapters, cards) {
    fmt.Printf("%d: %s — %d гл.\n", b.ID, b.Label(), b.Count)
}
// 9824: Silent Step & Эрл Грей — 550 гл.
// 9823: Kyu Team & Rulate Project & FiuTeam — 301 гл.
// 26435: Альтернативный перевод — 0 гл.

// 2. Задание: ветка, диапазон глав, иллюстрации
store, err := job.OpenStore(".rlib")
j, err := store.Plan(ctx, c, job.Request{
    Slug: manga.SlugURL, BranchID: 9824, From: 1, To: 100, WithImages: true,
})

// 3. Загрузка — останавливается на первой ошибке, продолжается с того же места
err = j.Download(ctx, c, job.DownloadOptions{
    OnChapter: func(e job.Event) {
        fmt.Printf("%d/%d %s (осталось ~%v)\n", e.Progress.Done, e.Progress.Total, e.Chapter.Title(), e.ETA)
    },
})
var chErr *job.ChapterError
if errors.As(err, &chErr) {
    fmt.Printf("остановились на главе %s: %v\n", chErr.Chapter.Number, chErr.Err)
}

// 4. Сборка книги — в сеть не ходит
opt, _ := imagex.NewMagick(filepath.Join(j.Dir(), "min"), 1200, 82)
res, err := j.BuildFile(ctx, "книга.epub", job.BuildOptions{Optimizer: opt})
```

## Темп запросов

Клиент сам держит паузу между запросами и ходит на сайт строго последовательно:
параллельных обращений не бывает даже при вызовах из разных горутин.

```go
c := ranobelib.New(
    ranobelib.WithThrottle(1500*time.Millisecond, 700*time.Millisecond), // пауза + случайный разброс
    ranobelib.WithRetries(4),
    ranobelib.WithNotifier(func(n ranobelib.Notice) { log.Println(n.Message) }),
)
```

На 429 и 503 пауза временно растёт (с оглядкой на `Retry-After`) и постепенно возвращается
к исходной. 4xx не повторяются: `errors.Is(err, ranobelib.ErrNotFound)` отличает платную или
удалённую главу от временной беды. Отмена через `context` работает и во время паузы.

## Загрузка и докачка

Задание — это каталог: `job.json`, сырые ответы по главам в `chapters/`, картинки в `assets/`.
Глава помечается выполненной сразу после записи на диск, поэтому обрыв не теряет предыдущие.

- `Download` возвращает `*job.ChapterError` — видно, на какой главе всё встало и почему;
- повторный вызов докачивает только недостающее;
- расширение диапазона в `Plan` не трогает уже скачанное;
- `Build` собирает книгу из кэша и в сеть не ходит вообще; недокачанные главы пропускаются
  и считаются в `BuildResult.Missing`.

## Разметка

Сайт отдаёт содержимое главы в двух видах — ProseMirror-документом и HTML-строкой,
и оба приводятся к одному XHTML. Неизвестные теги разворачиваются (текст не теряется),
скрипты и стили выбрасываются, незакрытые теги закрываются.

Картинки проходят через `content.ImageResolver` — он решает, что положить в `src` и
класть ли картинку вообще:

```go
body := chapter.Content.XHTML(content.Options{
    Attachments: chapter.Attachments,
    Images: content.ResolverFunc(func(img content.Image) (string, bool) {
        return "../images/" + save(img.URL), true
    }),
})
```

Аннотация книги приходит в тех же двух видах — `manga.Summary.PlainText()` разбирает оба.

## Сборка EPUB отдельно

`epub.Book` не знает про сайт: ему можно скормить любые главы.

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
- **сжатие только через ImageMagick**, без запасного пути на `sips`.

## Тесты

```sh
go test ./...                 # без сети, включая полный цикл на локальном сервере
go test -race ./...
RANOBELIB_LIVE=1 go test -run TestLive -v .   # живой прогон: две главы с сайта
```
