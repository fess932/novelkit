// Command novelkit downloads books into EPUB on top of the novelkit library.
//
// The library knows nothing about this program: the prompts, the flags and the
// output live here, while the work is done by the novel, job, epub and imagex
// packages.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/fess932/novelkit/novel"
	"github.com/fess932/novelkit/sources/ranobelib"
)

const help = `novelkit — download books into EPUB

Usage:
  novelkit                        menu: pick an action and a book from the cache
  novelkit <link|slug|title>      [options]
  novelkit --resume [job dir]
  novelkit --list-jobs

Options:
  --edition <id>      translation to download; otherwise pick from a list
  --edition-name <s>  translation by team name (substring)
  --list-editions     print the translations and exit
  --from <n>          first chapter by position (1 is the first)
  --to <n>            last chapter, inclusive
  --out <file>        path to the .epub (defaults to the book title)
  --work-dir <dir>    job cache directory (default .novelkit)
  --delay <ms>        base pause between requests (default 1500)
  --jitter <ms>       random addition to the pause (default 700)
  --retries <n>       retries on a network error or 429 (default 4)
  --no-images         skip illustrations
  --compress          shrink illustrations while assembling
  --max-image <px>    longer side for --compress (default 1200)
  --quality <1-100>   jpeg quality for --compress (default 82)
  --build-only        assemble the EPUB from the cache, downloading nothing
  --refresh-meta      refresh the blurb, author and genres from the book page
  --resume [dir]      continue an interrupted download
  --list-jobs         list the jobs in the cache
  --token <token>     access token of a signed-in account; some titles need one
                      (or set RANOBELIB_TOKEN)
  --cookie <cookie>   Cookie header instead, when the site authorises that way
                      (or set RANOBELIB_COOKIE)
  --yes               no questions (fullest translation, all chapters)

A download stops at the first error; continue it with novelkit --resume
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
	token       string
	cookie      string

	args []string
}

func main() {
	if err := run(); err != nil {
		if errors.Is(err, errHelp) {
			return // the help text has already been printed
		}
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "\nInterrupted.")
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

	// Ctrl+C must stop the download without losing what is already fetched.
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
	fs.StringVar(&o.token, "token", os.Getenv("RANOBELIB_TOKEN"), "")
	fs.StringVar(&o.cookie, "cookie", os.Getenv("RANOBELIB_COOKIE"), "")

	// The standard flag package gives up at the first non-flag, while a book
	// title reads better first. So the arguments are split by hand: flags go to
	// the parser, everything else is the book. --resume is special too — it
	// takes either a job directory or nothing at all.
	withValue := map[string]bool{
		"edition": true, "branch": true, "edition-name": true, "branch-name": true,
		"from": true, "to": true, "out": true, "work-dir": true,
		"delay": true, "jitter": true, "retries": true,
		"max-image": true, "quality": true, "token": true, "cookie": true,
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

// errHelp means "the help text was printed, now leave", not a failure.
var errHelp = errors.New("help")

// app ties the source, the job cache and the output together.
type app struct {
	opts   *options
	source novel.Source
	reg    *novel.Registry
}

func newApp(o *options) (*app, error) {
	src := ranobelib.NewSource(
		ranobelib.WithThrottle(time.Duration(o.delay)*time.Millisecond, time.Duration(o.jitter)*time.Millisecond),
		ranobelib.WithRetries(o.retries),
		ranobelib.WithToken(o.token),
		ranobelib.WithCookie(o.cookie),
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

// outputName picks the book's file name.
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

func workDir(o *options) string { return o.workDir }

// fmtDuration prints a duration in a compact human form.
func fmtDuration(d time.Duration) string {
	s := int(d.Round(time.Second).Seconds())
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm %ds", s/60, s%60)
	default:
		return fmt.Sprintf("%dh %dm", s/3600, (s%3600)/60)
	}
}
