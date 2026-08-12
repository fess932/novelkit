// Package job хранит скачанное на диске и умеет продолжать прерванную загрузку.
//
// Задание — это каталог: описание в job.json, сырые ответы по главам в chapters/
// и картинки в assets/. Каждая глава отмечается выполненной сразу после записи,
// поэтому обрыв на любой главе не теряет предыдущие: следующий запуск продолжит
// ровно с места остановки.
//
// Пакет не знает ни про один сайт: он работает с любым novel.Source.
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
	"strings"
	"sync"
	"time"

	"github.com/fess932/ranobelib/epub"
	"github.com/fess932/ranobelib/markup"
	"github.com/fess932/ranobelib/novel"
)

// Version — версия формата задания. Каталог другой версии читать нельзя.
const Version = 1

// SourceRef говорит, откуда и что качается.
type SourceRef struct {
	// ID источника, например "ranobelib". По нему задание находит нужный сайт.
	ID     string `json:"id"`
	BookID string `json:"book_id"`
	// EditionID — выбранный перевод; пустой, если у сайта переводов нет.
	EditionID    string `json:"edition_id"`
	EditionLabel string `json:"edition_label"`
	BookURL      string `json:"book_url"`
}

// Asset — скачанная картинка.
type Asset struct {
	File string `json:"file"` // имя файла внутри assets/
	Ext  string `json:"ext"`
}

// ChapterState — глава задания и признак того, что она уже скачана.
type ChapterState struct {
	ID     string `json:"id,omitempty"`
	Index  int    `json:"index"`
	Volume string `json:"volume"`
	Number string `json:"number"`
	Name   string `json:"name"`
	Done   bool   `json:"done"`
}

// Info возвращает главу в общем виде.
func (c ChapterState) Info() novel.ChapterInfo {
	return novel.ChapterInfo{ID: c.ID, Index: c.Index, Volume: c.Volume, Number: c.Number, Name: c.Name}
}

// Title собирает заголовок главы.
func (c ChapterState) Title() string { return c.Info().Title() }

// State — содержимое job.json.
type State struct {
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	Source     SourceRef        `json:"source"`
	WithImages bool             `json:"with_images"`
	Book       epub.Metadata    `json:"book"`
	Cover      *Asset           `json:"cover,omitempty"`
	Assets     map[string]Asset `json:"assets"` // ключ — адрес картинки
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
	// BookID — идентификатор книги внутри источника.
	BookID string
	// EditionID — выбранный перевод. Пустой означает единственный или безымянный.
	EditionID string
	// From и To — позиции глав внутри перевода, начиная с 1.
	// To <= 0 означает «до последней главы».
	From, To int
	// WithImages включает скачивание иллюстраций и обложки.
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
	jobs := make([]*Job, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		j, err := s.Open(filepath.Join(s.root, e.Name()))
		if err != nil {
			continue // чужой или недописанный каталог просто пропускаем
		}
		jobs = append(jobs, j)
	}
	sort.SliceStable(jobs, func(i, k int) bool {
		return jobs[i].state.UpdatedAt.After(jobs[k].state.UpdatedAt)
	})
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
// новые. Ходит в сеть за карточкой книги, списком глав и обложкой.
func (s *Store) Plan(ctx context.Context, src novel.Source, req Request) (*Job, error) {
	book, err := src.Book(ctx, req.BookID)
	if err != nil {
		return nil, err
	}
	chapters, err := src.Chapters(ctx, req.BookID, req.EditionID)
	if err != nil {
		return nil, err
	}
	if len(chapters) == 0 {
		return nil, fmt.Errorf("job: в выбранном переводе нет глав")
	}

	edition, ok := book.Edition(req.EditionID)
	if !ok {
		// У источника может не быть переводов как таковых — это нормально.
		edition = novel.Edition{ID: req.EditionID, Chapters: len(chapters)}
	}

	novel.SortChapters(chapters)
	from, to := req.From, req.To
	if from < 1 {
		from = 1
	}
	if to <= 0 || to > len(chapters) {
		to = len(chapters)
	}
	if from > to {
		return nil, fmt.Errorf("job: пустой диапазон глав %d–%d", from, to)
	}
	chapters = chapters[from-1 : to]

	dir := filepath.Join(s.root, dirName(src.ID(), req.BookID, req.EditionID))
	for _, sub := range []string{"chapters", "assets"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, err
		}
	}

	j, err := s.Open(dir)
	if err != nil {
		j = &Job{dir: dir, state: State{
			Version:   Version,
			CreatedAt: time.Now(),
			Assets:    map[string]Asset{},
		}}
	}

	// Обновление состояния держим в отдельной области видимости:
	// скачивание обложки ниже само берёт этот же мьютекс.
	if err := func() error {
		j.mu.Lock()
		defer j.mu.Unlock()

		// Скачанное сохраняем, новые главы добавляем.
		done := make(map[int]ChapterState, len(j.state.Chapters))
		for _, ch := range j.state.Chapters {
			done[ch.Index] = ch
		}
		next := make([]ChapterState, 0, len(chapters))
		for _, ci := range chapters {
			if prev, ok := done[ci.Index]; ok {
				next = append(next, prev)
				continue
			}
			next = append(next, ChapterState{
				ID: ci.ID, Index: ci.Index, Volume: ci.Volume, Number: ci.Number, Name: ci.Name,
			})
		}

		j.state.Version = Version
		j.state.Chapters = next
		j.state.WithImages = req.WithImages
		j.state.Source = SourceRef{
			ID:           src.ID(),
			BookID:       req.BookID,
			EditionID:    req.EditionID,
			EditionLabel: edition.Label(),
			BookURL:      book.URL,
		}
		j.state.Book = Metadata(book, edition)
		return j.save()
	}(); err != nil {
		return nil, err
	}

	if req.WithImages && book.CoverURL != "" {
		if err := j.fetchCover(ctx, src, book.CoverURL); err != nil {
			j.warn(fmt.Sprintf("обложка не скачалась: %v", err))
		}
	}
	return j, nil
}

// Metadata собирает метаданные книги из карточки источника и выбранного перевода.
func Metadata(b *novel.Book, edition novel.Edition) epub.Metadata {
	return epub.Metadata{
		Title:         b.Title,
		OriginalTitle: b.OriginalTitle,
		Language:      "ru",
		Authors:       b.Authors,
		Translators:   edition.Translators(),
		Genres:        b.Genres,
		Publisher:     b.Publisher,
		Date:          b.Year,
		Description:   b.Description,
		Source:        b.URL,
	}
}

// RefreshMetadata перечитывает карточку книги. Список глав не трогается,
// перевод остаётся прежним.
func (j *Job) RefreshMetadata(ctx context.Context, src novel.Source) error {
	st := j.State()
	book, err := src.Book(ctx, st.Source.BookID)
	if err != nil {
		return err
	}
	edition, ok := book.Edition(st.Source.EditionID)
	if !ok {
		edition = novel.Edition{Teams: st.Book.Translators}
	}

	j.mu.Lock()
	defer j.mu.Unlock()
	j.state.Book = Metadata(book, edition)
	return j.save()
}

func (j *Job) fetchCover(ctx context.Context, src novel.Source, url string) error {
	data, _, err := src.Fetch(ctx, url)
	if err != nil {
		return err
	}
	ext := extOf(url)
	name := "cover." + ext
	if err := writeFileAtomic(j.assetPath(name), data); err != nil {
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

func dirName(sourceID, bookID, editionID string) string {
	name := unsafeName.ReplaceAllString(sourceID+"-"+bookID, "_")
	if editionID == "" {
		return name + "--default"
	}
	return name + "--" + unsafeName.ReplaceAllString(editionID, "_")
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
	if e := markup.ExtOf(u); e != "" {
		return e
	}
	return "jpg"
}

// trimSpace вынесен, чтобы не тянуть strings ради одного вызова в других файлах пакета.
func trimSpace(s string) string { return strings.TrimSpace(s) }
