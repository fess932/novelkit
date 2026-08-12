package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/fess932/novelkit/imagex"
	"github.com/fess932/novelkit/job"
	"github.com/fess932/novelkit/novel"
)

// fresh обрабатывает запуск с книгой в аргументах: ссылка, слаг или название.
func (a *app) fresh(ctx context.Context, input string) error {
	bookID, err := a.resolveBook(ctx, input)
	if err != nil {
		return err
	}

	fmt.Println("Читаю карточку книги…")
	book, err := a.source.Book(ctx, bookID)
	if err != nil {
		return err
	}

	if a.opts.listEds {
		return a.showEditions(book)
	}

	edition, err := a.chooseEdition(book)
	if err != nil {
		return err
	}
	fmt.Printf("Перевод: %s — %d гл.\n", edition.Label(), edition.Chapters)

	from, to, err := a.chooseRange(ctx, bookID, edition)
	if err != nil {
		return err
	}

	store, err := job.OpenStore(workDir(a.opts))
	if err != nil {
		return err
	}
	j, err := store.Plan(ctx, a.source, job.Request{
		BookID:     bookID,
		EditionID:  edition.ID,
		From:       from,
		To:         to,
		WithImages: !a.opts.noImages,
	})
	if err != nil {
		return err
	}

	out := a.opts.out
	if out == "" {
		out = outputName(book.Title, edition.Label(), len(book.Editions) > 1)
	}
	return a.process(ctx, j, out)
}

// resolveBook превращает ссылку, слаг или название в идентификатор книги.
func (a *app) resolveBook(ctx context.Context, input string) (string, error) {
	if _, id, err := a.reg.Resolve(input); err == nil {
		return id, nil
	}

	fmt.Printf("Ищу «%s»…\n", input)
	found, err := a.source.Search(ctx, input)
	if err != nil {
		return "", err
	}
	if len(found) == 0 {
		return "", fmt.Errorf("по запросу «%s» ничего не нашлось", input)
	}
	if a.opts.yes || !interactive() {
		fmt.Printf("Выбрано: %s\n", found[0].Title)
		return found[0].ID, nil
	}

	items := make([]Item, 0, min(len(found), 15))
	for _, b := range found[:min(len(found), 15)] {
		items = append(items, Item{Label: b.Title, Hint: "(" + b.OriginalTitle + ")"})
	}
	idx, err := selectItem("Что скачиваем?", items, 0)
	if err != nil {
		return "", err
	}
	return found[idx].ID, nil
}

func (a *app) showEditions(book *novel.Book) error {
	fmt.Printf("\n%s — переводов: %d\n", book.Title, len(book.Editions))
	for _, e := range book.Editions {
		count := fmt.Sprintf("%d гл.", e.Chapters)
		if e.Chapters == 0 {
			count = "нет глав"
		}
		own := ""
		if e.Name != "" && e.Name != e.Label() {
			own = fmt.Sprintf(" («%s»)", e.Name)
		}
		who := ""
		if len(e.Uploaders) > 0 {
			who = " [" + strings.Join(e.Uploaders[:min(len(e.Uploaders), 3)], ", ") + "]"
		}
		id := e.ID
		if id == "" {
			id = "(нет)"
		}
		fmt.Printf("  id=%s  %s  %s%s%s\n", id, count, e.Label(), own, who)
	}
	return nil
}

// chooseEdition выбирает перевод: по флагу, по названию команды или меню.
func (a *app) chooseEdition(book *novel.Book) (novel.Edition, error) {
	usable := make([]novel.Edition, 0, len(book.Editions))
	for _, e := range book.Editions {
		if e.Chapters > 0 {
			usable = append(usable, e)
		}
	}
	empty := len(book.Editions) - len(usable)

	switch {
	case len(book.Editions) == 0:
		// У источника переводов может не быть вовсе.
		return novel.Edition{}, nil
	case len(usable) == 0:
		return novel.Edition{}, fmt.Errorf("ни в одном переводе нет глав")
	}

	if id := a.opts.edition; id != "" {
		for _, e := range book.Editions {
			if e.ID == id {
				if e.Chapters == 0 {
					return novel.Edition{}, fmt.Errorf("в переводе %s (%s) нет глав", id, e.Label())
				}
				return e, nil
			}
		}
		return novel.Edition{}, fmt.Errorf("перевод %s у этой книги не найден", id)
	}

	if needle := strings.ToLower(a.opts.editionName); needle != "" {
		for _, e := range usable {
			if strings.Contains(strings.ToLower(e.Label()), needle) ||
				strings.Contains(strings.ToLower(e.Name), needle) {
				return e, nil
			}
		}
		return novel.Edition{}, fmt.Errorf("перевод со словом «%s» не найден", a.opts.editionName)
	}

	if len(usable) == 1 {
		return usable[0], nil
	}
	if a.opts.yes || !interactive() {
		fmt.Printf("Переводов: %d; беру самый полный — %s\n", len(usable), usable[0].Label())
		return usable[0], nil
	}
	if empty > 0 {
		fmt.Printf("\n(ещё %d вкладк%s на сайте без глав — пропускаю)\n", empty, plural(empty, "а", "и", ""))
	}

	items := make([]Item, 0, len(usable))
	for _, e := range usable {
		hint := fmt.Sprintf("— %d гл.", e.Chapters)
		if len(e.Uploaders) > 0 {
			hint += ", залил " + strings.Join(e.Uploaders[:min(len(e.Uploaders), 2)], ", ")
		}
		items = append(items, Item{Label: e.Label(), Hint: hint})
	}
	idx, err := selectItem("Чей перевод скачиваем?", items, 0)
	if err != nil {
		return novel.Edition{}, err
	}
	return usable[idx], nil
}

// chooseRange спрашивает диапазон глав, если он не задан флагами.
func (a *app) chooseRange(ctx context.Context, bookID string, edition novel.Edition) (int, int, error) {
	from, to := a.opts.from, a.opts.to
	if from != 0 || to != 0 || a.opts.yes || !interactive() {
		return from, to, nil
	}

	total := edition.Chapters
	if total == 0 {
		list, err := a.source.Chapters(ctx, bookID, edition.ID)
		if err != nil {
			return 0, 0, err
		}
		total = len(list)
	}

	answer := ask(fmt.Sprintf("Диапазон глав 1–%d (Enter — все, например 1-100): ", total), "")
	if answer == "" {
		return 0, 0, nil
	}
	return parseRange(answer)
}

// parseRange понимает «1-100», «1..100» и «50» (с 50-й до конца).
func parseRange(s string) (int, int, error) {
	s = strings.NewReplacer("–", "-", "..", "-", " ", "").Replace(strings.TrimSpace(s))
	left, right, ok := strings.Cut(s, "-")

	from, err := atoiPositive(left)
	if err != nil {
		return 0, 0, fmt.Errorf("не понял диапазон «%s»", s)
	}
	if !ok || right == "" {
		return from, 0, nil
	}
	to, err := atoiPositive(right)
	if err != nil {
		return 0, 0, fmt.Errorf("не понял диапазон «%s»", s)
	}
	return from, to, nil
}

func atoiPositive(s string) (int, error) {
	n := 0
	if s == "" {
		return 0, fmt.Errorf("пусто")
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("не число")
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}

// resume продолжает прерванное задание.
func (a *app) resume(ctx context.Context) error {
	store, err := job.OpenStore(workDir(a.opts))
	if err != nil {
		return err
	}

	var j *job.Job
	if a.opts.resume != "" {
		j, err = store.Open(a.opts.resume)
		if err != nil {
			return fmt.Errorf("в %s нет сохранённого задания: %w", a.opts.resume, err)
		}
	} else {
		j, err = a.pickJob(store, false)
		if err != nil {
			return err
		}
	}

	out := a.opts.out
	if out == "" {
		st := j.State()
		out = outputName(st.Book.Title, st.Source.EditionLabel, true)
	}
	return a.process(ctx, j, out)
}

// pickJob выбирает задание из кэша.
func (a *app) pickJob(store *job.Store, onlyUnfinished bool) (*job.Job, error) {
	jobs, err := store.List()
	if err != nil {
		return nil, err
	}
	if onlyUnfinished {
		left := jobs[:0]
		for _, j := range jobs {
			if j.Progress().Left() > 0 {
				left = append(left, j)
			}
		}
		jobs = left
	}
	if len(jobs) == 0 {
		return nil, fmt.Errorf("в %s нет заданий", workDir(a.opts))
	}
	if len(jobs) == 1 || a.opts.yes || !interactive() {
		return jobs[0], nil
	}

	items := make([]Item, 0, len(jobs))
	for _, j := range jobs {
		st, p := j.State(), j.Progress()
		items = append(items, Item{
			Label: st.Book.Title,
			Hint:  fmt.Sprintf("— %d/%d гл., %s", p.Done, p.Total, st.Source.EditionLabel),
		})
	}
	idx, err := selectItem("Какую книгу берём?", items, 0)
	if err != nil {
		return nil, err
	}
	return jobs[idx], nil
}

// process качает недостающее (если не --build-only) и собирает книгу.
func (a *app) process(ctx context.Context, j *job.Job, out string) error {
	if a.opts.refreshMeta {
		if err := j.RefreshMetadata(ctx, a.source); err != nil {
			fmt.Printf("  ! метаданные обновить не вышло: %v\n", err)
		} else {
			fmt.Println("Метаданные обновлены из карточки книги.")
		}
	}

	st, p := j.State(), j.Progress()
	fmt.Printf("\nКнига: %s\n", st.Book.Title)
	if st.Source.EditionLabel != "" {
		fmt.Printf("Перевод: %s\n", st.Source.EditionLabel)
	}
	fmt.Printf("Глав в задании: %d", p.Total)
	if p.Done > 0 {
		fmt.Printf(" (уже скачано %d)", p.Done)
	}
	fmt.Printf("\nКэш: %s\n", j.Dir())

	if !a.opts.buildOnly {
		fmt.Printf("Пауза между запросами: %d–%d мс\n\n", a.opts.delay, a.opts.delay+a.opts.jitter)
		err := j.Download(ctx, a.source, job.DownloadOptions{
			OnChapter: func(e job.Event) {
				line := fmt.Sprintf("  [%3d%%] %d/%d  %s",
					e.Progress.Done*100/max(e.Progress.Total, 1), e.Progress.Done, e.Progress.Total, e.Chapter.Title())
				if e.ETA > 0 {
					line += "  ~" + fmtDuration(e.ETA)
				}
				fmt.Println(line)
			},
			OnWarning: func(msg string) { fmt.Printf("  ! %s\n", msg) },
		})
		if err != nil {
			var chErr *job.ChapterError
			p := j.Progress()
			fmt.Printf("\n✗ Загрузка остановлена: %v\n", err)
			if errors.As(err, &chErr) {
				fmt.Printf("  на главе: %s (том %s)\n", chErr.Chapter.Number, chErr.Chapter.Volume)
			}
			fmt.Printf("  скачано %d/%d, осталось %d\n", p.Done, p.Total, p.Left())
			fmt.Printf("  продолжить: novelkit --resume %s\n", j.Dir())
			fmt.Println("  если ошибка из-за рейт-лимита, добавьте --delay 4000")
			return errors.New("загрузка не завершена")
		}
	}

	fmt.Println("\nСобираю EPUB…")
	opts := job.BuildOptions{
		OnWarning: func(msg string) { fmt.Printf("  ! %s\n", msg) },
	}
	if a.opts.compress {
		dir := filepath.Join(j.Dir(), fmt.Sprintf("min-%d-%d", a.opts.maxImage, a.opts.quality))
		opt, err := imagex.NewResizer(dir, a.opts.maxImage, a.opts.quality)
		if err != nil {
			return err
		}
		opts.Optimizer = opt
		fmt.Printf("  сжатие иллюстраций: до %d px по большей стороне, качество %d\n", a.opts.maxImage, a.opts.quality)
	}

	res, err := j.BuildFile(ctx, a.source, out, opts)
	if err != nil {
		return err
	}
	if a.opts.compress && res.ImagesBefore > 0 {
		fmt.Printf("  иллюстрации: %.1f → %.1f МБ (−%.0f%%)\n",
			mb(res.ImagesBefore), mb(res.ImagesAfter),
			(1-float64(res.ImagesAfter)/float64(res.ImagesBefore))*100)
	}
	fmt.Printf("✓ %s — %d гл., %d илл., %.2f МБ\n", out, res.Chapters, res.Images, mb(res.Size))
	if n := len(j.State().Warnings); n > 0 {
		fmt.Printf("  предупреждений: %d (см. job.json)\n", n)
	}
	return nil
}

func (a *app) showJobs() error {
	store, err := job.OpenStore(workDir(a.opts))
	if err != nil {
		return err
	}
	jobs, err := store.List()
	if err != nil {
		return err
	}
	if len(jobs) == 0 {
		fmt.Printf("Заданий в %s нет.\n", workDir(a.opts))
		return nil
	}
	for _, j := range jobs {
		st, p := j.State(), j.Progress()
		fmt.Printf("%s\n    %s — %d/%d гл., перевод: %s\n", j.Dir(), st.Book.Title, p.Done, p.Total, st.Source.EditionLabel)
	}
	return nil
}

// menu — запуск без аргументов: выбор действия и книги.
func (a *app) menu(ctx context.Context) error {
	store, err := job.OpenStore(workDir(a.opts))
	if err != nil {
		return err
	}
	jobs, _ := store.List()
	unfinished := 0
	for _, j := range jobs {
		if j.Progress().Left() > 0 {
			unfinished++
		}
	}

	type action struct {
		key  string
		item Item
	}
	actions := []action{{"new", Item{"Скачать новую книгу", "— по ссылке или названию"}}}
	if unfinished > 0 {
		actions = append(actions, action{"resume", Item{"Продолжить загрузку", fmt.Sprintf("— незавершённых: %d", unfinished)}})
	}
	if len(jobs) > 0 {
		actions = append(actions,
			action{"build", Item{"Собрать EPUB из кэша", fmt.Sprintf("— книг в кэше: %d", len(jobs))}},
			action{"list", Item{"Показать, что в кэше", ""}})
	}

	items := make([]Item, len(actions))
	for i, act := range actions {
		items[i] = act.item
	}
	idx, err := selectItem("Что делаем?", items, 0)
	if err != nil {
		return err
	}

	switch actions[idx].key {
	case "list":
		return a.showJobs()
	case "new":
		input := ask("\nСсылка, slug или название книги: ", "")
		if input == "" {
			fmt.Println("Пусто — выхожу.")
			return nil
		}
		a.opts.compress = a.askCompress()
		return a.fresh(ctx, input)
	default:
		j, err := a.pickJob(store, actions[idx].key == "resume")
		if err != nil {
			return err
		}
		a.opts.buildOnly = actions[idx].key == "build"
		a.opts.compress = a.askCompress()

		out := a.opts.out
		if out == "" {
			st := j.State()
			out = outputName(st.Book.Title, st.Source.EditionLabel, true)
		}
		return a.process(ctx, j, out)
	}
}

func (a *app) askCompress() bool {
	if a.opts.compress {
		return true
	}
	return confirm("\nСжать иллюстрации? (книга станет в разы легче)", true)
}

func mb(n int64) float64 { return float64(n) / 1024 / 1024 }

func plural(n int, one, few, many string) string {
	switch {
	case n%10 == 1 && n%100 != 11:
		return one
	case n%10 >= 2 && n%10 <= 4 && (n%100 < 10 || n%100 >= 20):
		return few
	default:
		return many
	}
}
