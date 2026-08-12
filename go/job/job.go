// Package job хранит скачанное на диске и умеет продолжать прерванную загрузку.
//
// Задание — это каталог: описание в job.json, сырые ответы по главам в chapters/
// и картинки в assets/. Каждая глава отмечается выполненной сразу после записи,
// поэтому обрыв на любой главе не теряет предыдущие: следующий запуск продолжит
// ровно с места остановки.
package job

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fess932/ranobelib"
	"github.com/fess932/ranobelib/epub"
)

// Version — версия формата задания. Каталог другой версии читать нельзя.
const Version = 1

// Source описывает, что именно качается.
type Source struct {
	Slug        string `json:"slug"`
	URL         string `json:"url"`
	BranchID    int    `json:"branch_id"`
	BranchLabel string `json:"branch_label"`
}

// Asset — скачанная картинка.
type Asset struct {
	File string `json:"file"` // имя файла внутри assets/
	Ext  string `json:"ext"`
}

// ChapterState — глава задания и признак того, что она уже скачана.
type ChapterState struct {
	Index  int    `json:"index"`
	Volume string `json:"volume"`
	Number string `json:"number"`
	Name   string `json:"name"`
	Done   bool   `json:"done"`
}

// Title собирает заголовок главы.
func (c ChapterState) Title() string {
	return ranobelib.ChapterInfo{Number: c.Number, Name: c.Name}.Title()
}

// State — содержимое job.json.
type State struct {
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	Source     Source           `json:"source"`
	WithImages bool             `json:"with_images"`
	Book       epub.Metadata    `json:"book"`
	Cover      *Asset           `json:"cover,omitempty"`
	Assets     map[string]Asset `json:"assets"` // ключ — абсолютный адрес картинки
	Chapters   []ChapterState   `json:"chapters"`
	Warnings   []string         `json:"warnings,omitempty"`
}

// Progress — сколько глав готово.
type Progress struct {
	Done  int
	Total int
}

// Left возвращает число оставшихся глав.
func (p Progress) Left() int { return p.Total - p.Done }

// Request описывает, что нужно скачать.
type Request struct {
	// Slug книги, например "14841--beginning-after-the-end-novel".
	Slug string
	// BranchID — ветка перевода; 0 означает ветку без идентификатора.
	BranchID int
	// From и To — позиции глав внутри ветки, начиная с 1.
	// To <= 0 означает «до последней главы».
	From, To int
	// WithImages включает скачивание иллюстраций.
	WithImages bool
}

// Store — каталог, в котором лежат задания.
type Store struct {
	root string
}

// OpenStore открывает (и при необходимости создаёт) каталог заданий.
func OpenStore(root string) (*Store, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &Store{root: root}, nil
}

// Root возвращает путь к каталогу заданий.
func (s *Store) Root() string { return s.root }

// Job — одно задание.
type Job struct {
	dir string

	mu    sync.Mutex
	state State
}

// Dir возвращает каталог задания.
func (j *Job) Dir() string { return j.dir }

// State отдаёт копию состояния: менять его снаружи нельзя.
func (j *Job) State() State {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.state.clone()
}

// Progress сообщает, сколько глав уже скачано.
func (j *Job) Progress() Progress {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.state.progress()
}

func (s State) progress() Progress {
	p := Progress{Total: len(s.Chapters)}
	for _, c := range s.Chapters {
		if c.Done {
			p.Done++
		}
	}
	return p
}

func (s State) clone() State {
	out := s
	out.Chapters = append([]ChapterState(nil), s.Chapters...)
	out.Warnings = append([]string(nil), s.Warnings...)
	out.Assets = make(map[string]Asset, len(s.Assets))
	for k, v := range s.Assets {
		out.Assets[k] = v
	}
	return out
}

// List возвращает все задания каталога, свежие первыми.
func (s *Store) List() ([]*Job, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, err
	}
	type dated struct {
		job *Job
		at  time.Time
	}
	found := make([]dated, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		j, err := s.Open(filepath.Join(s.root, e.Name()))
		if err != nil {
			continue // чужой или недописанный каталог просто пропускаем
		}
		found = append(found, dated{j, j.state.UpdatedAt})
	}
	sort.SliceStable(found, func(i, k int) bool { return found[i].at.After(found[k].at) })

	jobs := make([]*Job, len(found))
	for i, f := range found {
		jobs[i] = f.job
	}
	return jobs, nil
}

// Open читает задание из каталога.
func (s *Store) Open(dir string) (*Job, error) {
	data, err := os.ReadFile(filepath.Join(dir, "job.json"))
	if err != nil {
		return nil, err
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("job: %s: %w", dir, err)
	}
	if st.Version != Version {
		return nil, fmt.Errorf("job: %s: формат версии %d, поддерживается %d", dir, st.Version, Version)
	}
	if st.Assets == nil {
		st.Assets = map[string]Asset{}
	}
	return &Job{dir: dir, state: st}, nil
}

// Plan создаёт задание или дополняет существующее.
//
// Уже скачанные главы не трогаются: если расширить диапазон, докачаются только
// новые. Требует трёх запросов к сайту — карточка книги, список глав и ветки.
func (s *Store) Plan(ctx context.Context, c *ranobelib.Client, req Request) (*Job, error) {
	manga, err := c.Manga(ctx, req.Slug)
	if err != nil {
		return nil, err
	}
	chapters, err := c.Chapters(ctx, req.Slug)
	if err != nil {
		return nil, err
	}
	cards, _ := c.Branches(ctx, manga.ID) // необязательно: без них останутся подписи по главам

	branches := ranobelib.CollectBranches(chapters, cards)
	var branch ranobelib.Branch
	for _, b := range branches {
		if b.ID == req.BranchID {
			branch = b
			break
		}
	}
	if branch.Count == 0 {
		return nil, fmt.Errorf("job: в ветке %d нет глав", req.BranchID)
	}

	list := ranobelib.ChaptersOfBranch(chapters, req.BranchID)
	from, to := req.From, req.To
	if from < 1 {
		from = 1
	}
	if to <= 0 || to > len(list) {
		to = len(list)
	}
	if from > to {
		return nil, fmt.Errorf("job: пустой диапазон глав %d–%d", from, to)
	}
	list = list[from-1 : to]

	dir := filepath.Join(s.root, dirName(req.Slug, req.BranchID))
	for _, sub := range []string{"chapters", "assets"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, err
		}
	}

	job, err := s.Open(dir)
	if err != nil {
		job = &Job{dir: dir, state: State{
			Version:   Version,
			CreatedAt: time.Now(),
			Assets:    map[string]Asset{},
		}}
	}

	// Обновление состояния держим в отдельной области видимости:
	// скачивание обложки ниже само берёт этот же мьютекс.
	if err := func() error {
		job.mu.Lock()
		defer job.mu.Unlock()

		// Скачанное сохраняем, новые главы добавляем.
		done := make(map[int]ChapterState, len(job.state.Chapters))
		for _, ch := range job.state.Chapters {
			done[ch.Index] = ch
		}
		next := make([]ChapterState, 0, len(list))
		for _, ci := range list {
			if prev, ok := done[ci.Index]; ok {
				next = append(next, prev)
				continue
			}
			next = append(next, ChapterState{Index: ci.Index, Volume: ci.Volume, Number: ci.Number, Name: ci.Name})
		}

		job.state.Version = Version
		job.state.Chapters = next
		job.state.WithImages = req.WithImages
		job.state.Source = Source{
			Slug:        req.Slug,
			URL:         manga.URL(c.SiteURL()),
			BranchID:    req.BranchID,
			BranchLabel: branch.Label(),
		}
		job.state.Book = Metadata(manga, branch, c.SiteURL())
		return job.save()
	}(); err != nil {
		return nil, err
	}
	if req.WithImages {
		if err := job.fetchCover(ctx, c, manga); err != nil {
			job.warn(fmt.Sprintf("обложка не скачалась: %v", err))
		}
	}
	return job, nil
}

// Metadata собирает метаданные книги из карточки сайта и ветки перевода.
func Metadata(m *ranobelib.Manga, branch ranobelib.Branch, site string) epub.Metadata {
	title := m.Title()
	original := m.EngName
	if original == title {
		original = m.Name
	}
	publisher := ""
	if len(m.Publisher) > 0 {
		publisher = m.Publisher[0].Title()
	}
	return epub.Metadata{
		Title:         title,
		OriginalTitle: original,
		Language:      "ru",
		Authors:       m.AuthorNames(),
		Translators:   branch.Translators(),
		Genres:        m.GenreNames(),
		Publisher:     publisher,
		Date:          m.ReleaseDate,
		Description:   m.Summary.PlainText(),
		Source:        m.URL(site),
	}
}

// RefreshMetadata перечитывает карточку книги: один запрос.
// Ветка перевода и список глав не трогаются.
func (j *Job) RefreshMetadata(ctx context.Context, c *ranobelib.Client) error {
	manga, err := c.Manga(ctx, j.state.Source.Slug)
	if err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()

	translators := j.state.Book.Translators
	j.state.Book = Metadata(manga, ranobelib.Branch{}, c.SiteURL())
	j.state.Book.Translators = translators
	return j.save()
}

func (j *Job) fetchCover(ctx context.Context, c *ranobelib.Client, m *ranobelib.Manga) error {
	url := m.Cover.URL()
	if url == "" {
		return nil
	}
	data, _, err := c.Fetch(ctx, url)
	if err != nil {
		return err
	}
	ext := extOf(url)
	name := "cover." + ext
	if err := writeFileAtomic(filepath.Join(j.dir, "assets", name), data); err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.state.Cover = &Asset{File: name, Ext: ext}
	return j.save()
}

// save пишет job.json. Вызывается под j.mu.
func (j *Job) save() error {
	j.state.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(j.state, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(j.dir, "job.json"), data)
}

func (j *Job) warn(msg string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.state.Warnings = append(j.state.Warnings, msg)
}

// writeFileAtomic пишет через временный файл: обрыв не оставит битого job.json.
func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (j *Job) chapterPath(index int) string {
	return filepath.Join(j.dir, "chapters", fmt.Sprintf("%05d.json", index))
}

func (j *Job) assetPath(name string) string { return filepath.Join(j.dir, "assets", name) }

var unsafeName = regexp.MustCompile(`[^\w.-]+`)

func dirName(slug string, branchID int) string {
	name := unsafeName.ReplaceAllString(slug, "_")
	if branchID == 0 {
		return name + "--default"
	}
	return name + "--b" + strconv.Itoa(branchID)
}

// assetName даёт имя, устойчивое между запусками: зависит только от адреса.
func assetName(url, ext string) string {
	sum := sha1.Sum([]byte(url))
	if ext == "" {
		ext = "jpg"
	}
	return "img-" + hex.EncodeToString(sum[:])[:12] + "." + ext
}

func extOf(u string) string {
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	if i := strings.LastIndex(u, "."); i >= 0 && len(u)-i <= 6 {
		return strings.ToLower(u[i+1:])
	}
	return "jpg"
}
