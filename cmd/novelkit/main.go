// Команда novelkit — скачивание книг в EPUB поверх библиотеки novelkit.
//
// Библиотека про эту программу ничего не знает: весь интерактив, флаги и вывод
// живут здесь, а вся работа делается пакетами novel, job, epub и imagex.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/fess932/novelkit/novel"
	"github.com/fess932/novelkit/sources/ranobelib"
)

const help = `novelkit — скачивание книг в EPUB

Использование:
  novelkit                          меню: выбрать действие и книгу из кэша
  novelkit <ссылка|slug|название>   [опции]
  novelkit --resume [каталог задания]
  novelkit --list-jobs

Опции:
  --edition <id>      перевод (у ranobelib это ветка); иначе выбор из списка
  --edition-name <ст> перевод по названию команды (подстрока)
  --list-editions     показать переводы и выйти
  --from <n>          с какой главы по счёту (1 — первая)
  --to <n>            по какую главу включительно
  --out <файл>        путь к .epub (по умолчанию — по названию книги)
  --work-dir <кат>    каталог кэша заданий (по умолчанию .novelkit)
  --delay <мс>        базовая пауза между запросами (по умолчанию 1500)
  --jitter <мс>       случайная добавка к паузе (по умолчанию 700)
  --retries <n>       повторов при сетевой ошибке или 429 (по умолчанию 4)
  --no-images         не скачивать иллюстрации
  --compress          сжать иллюстрации при сборке
  --max-image <px>    большая сторона картинки при --compress (по умолчанию 1200)
  --quality <1-100>   качество jpeg при --compress (по умолчанию 82)
  --build-only        собрать EPUB из уже скачанного кэша
  --refresh-meta      обновить описание, автора и жанры из карточки книги
  --resume [каталог]  продолжить прерванную загрузку
  --list-jobs         показать задания в кэше
  --yes               без вопросов (берётся самый полный перевод и все главы)

Загрузка останавливается на первой ошибке; продолжить — novelkit --resume
`

type options struct {
	edition     string
	editionName string
	listEds     bool
	listJobs    bool
	from, to    int
	out         string
	workDir     string
	delay       int
	jitter      int
	retries     int
	noImages    bool
	compress    bool
	maxImage    int
	quality     int
	buildOnly   bool
	refreshMeta bool
	resume      string
	resumeSet   bool
	yes         bool

	args []string
}

func main() {
	if err := run(); err != nil {
		if errors.Is(err, errHelp) {
			return // справку уже показали
		}
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "\nПрервано.")
			os.Exit(130)
		}
		fmt.Fprintf(os.Stderr, "\n✗ %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	opts, err := parseFlags()
	if err != nil {
		return err
	}

	// Ctrl+C должен останавливать загрузку, не теряя скачанного.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	app, err := newApp(opts)
	if err != nil {
		return err
	}
	return app.run(ctx)
}

func parseFlags() (*options, error) {
	o := &options{}
	fs := flag.NewFlagSet("novelkit", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Print(help) }

	fs.StringVar(&o.edition, "edition", "", "")
	fs.StringVar(&o.edition, "branch", "", "")
	fs.StringVar(&o.editionName, "edition-name", "", "")
	fs.StringVar(&o.editionName, "branch-name", "", "")
	fs.BoolVar(&o.listEds, "list-editions", false, "")
	fs.BoolVar(&o.listEds, "list-branches", false, "")
	fs.BoolVar(&o.listJobs, "list-jobs", false, "")
	fs.IntVar(&o.from, "from", 0, "")
	fs.IntVar(&o.to, "to", 0, "")
	fs.StringVar(&o.out, "out", "", "")
	fs.StringVar(&o.workDir, "work-dir", ".novelkit", "")
	fs.IntVar(&o.delay, "delay", 1500, "")
	fs.IntVar(&o.jitter, "jitter", 700, "")
	fs.IntVar(&o.retries, "retries", 4, "")
	fs.BoolVar(&o.noImages, "no-images", false, "")
	fs.BoolVar(&o.compress, "compress", false, "")
	fs.IntVar(&o.maxImage, "max-image", 1200, "")
	fs.IntVar(&o.quality, "quality", 82, "")
	fs.BoolVar(&o.buildOnly, "build-only", false, "")
	fs.BoolVar(&o.refreshMeta, "refresh-meta", false, "")
	fs.BoolVar(&o.yes, "yes", false, "")

	// Стандартный flag бросает разбор на первом не-флаге, а название книги
	// удобнее писать первым. Поэтому раскладываем аргументы сами:
	// флаги идут в разбор, всё остальное — в название книги.
	// Заодно --resume может идти и без значения, и с каталогом задания.
	withValue := map[string]bool{
		"edition": true, "branch": true, "edition-name": true, "branch-name": true,
		"from": true, "to": true, "out": true, "work-dir": true,
		"delay": true, "jitter": true, "retries": true,
		"max-image": true, "quality": true,
	}

	var flags []string
	for i := 1; i < len(os.Args); i++ {
		a := os.Args[i]
		if !strings.HasPrefix(a, "-") {
			o.args = append(o.args, a)
			continue
		}

		name, _, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if name == "resume" {
			o.resumeSet = true
			if !hasValue && i+1 < len(os.Args) && !strings.HasPrefix(os.Args[i+1], "-") {
				i++
				o.resume = os.Args[i]
			}
			continue
		}

		flags = append(flags, a)
		if !hasValue && withValue[name] && i+1 < len(os.Args) {
			i++
			flags = append(flags, os.Args[i])
		}
	}

	if err := fs.Parse(flags); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, errHelp
		}
		return nil, err
	}
	o.args = append(o.args, fs.Args()...)
	return o, nil
}

// errHelp означает «показали справку и уходим», а не ошибку.
var errHelp = errors.New("help")

// app связывает вместе источник, кэш заданий и вывод.
type app struct {
	opts   *options
	source novel.Source
	reg    *novel.Registry
}

func newApp(o *options) (*app, error) {
	src := ranobelib.NewSource(
		ranobelib.WithThrottle(time.Duration(o.delay)*time.Millisecond, time.Duration(o.jitter)*time.Millisecond),
		ranobelib.WithRetries(o.retries),
		ranobelib.WithNotifier(func(n ranobelib.Notice) {
			fmt.Printf("  · %s\n", n.Message)
		}),
	)
	reg := &novel.Registry{}
	reg.Register(src)
	return &app{opts: o, source: src, reg: reg}, nil
}

func (a *app) run(ctx context.Context) error {
	o := a.opts

	switch {
	case o.listJobs:
		return a.showJobs()
	case o.resumeSet:
		return a.resume(ctx)
	case len(o.args) > 0:
		return a.fresh(ctx, strings.Join(o.args, " "))
	}

	if !interactive() {
		fmt.Print(help)
		return nil
	}
	return a.menu(ctx)
}

// outputName подбирает имя файла книги.
func outputName(title, edition string, many bool) string {
	name := safeFileName(title)
	if many && edition != "" {
		name += " [" + safeFileName(edition) + "]"
	}
	return name + ".epub"
}

func safeFileName(s string) string {
	replacer := strings.NewReplacer("/", "", "\\", "", "?", "", "%", "", "*", "", ":", "", "|", "", `"`, "", "<", "", ">", "")
	out := strings.Join(strings.Fields(replacer.Replace(s)), " ")
	if r := []rune(out); len(r) > 120 {
		out = string(r[:120])
	}
	return out
}

func workDir(o *options) string {
	if filepath.IsAbs(o.workDir) {
		return o.workDir
	}
	return o.workDir
}

// fmtDuration печатает длительность по-русски.
func fmtDuration(d time.Duration) string {
	s := int(d.Round(time.Second).Seconds())
	switch {
	case s < 60:
		return fmt.Sprintf("%d с", s)
	case s < 3600:
		return fmt.Sprintf("%d мин %d с", s/60, s%60)
	default:
		return fmt.Sprintf("%d ч %d мин", s/3600, (s%3600)/60)
	}
}
