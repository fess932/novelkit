package novel

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// ErrNotFound возвращается, когда книги или главы не существует:
// неверный идентификатор, удалённая или платная глава.
var ErrNotFound = errors.New("novel: не найдено")

// ErrUnsupported возвращается, когда ссылку не берёт ни один источник.
var ErrUnsupported = errors.New("novel: сайт не поддерживается")

// Source — сайт, откуда качаются книги.
//
// Всё, что нужно для поддержки нового сайта: реализовать этот интерфейс.
// Остальное (кэш, докачка, сборка EPUB, сжатие картинок) уже написано и
// работает с любым источником одинаково.
//
// Реализация обязана сама выдерживать вежливый темп запросов: ядро за неё
// этого не делает, а сайты за частые обращения закрывают доступ.
type Source interface {
	// ID — короткое имя источника, например "ranobelib".
	// Оно попадает в кэш задания, поэтому менять его нельзя.
	ID() string

	// Supports сообщает, берётся ли источник за такую ссылку.
	Supports(rawURL string) bool

	// ParseRef достаёт идентификатор книги из ссылки на сайт.
	ParseRef(rawURL string) (bookID string, ok bool)

	// Search ищет книги по названию. Источник, где поиска нет,
	// вправе вернуть ErrUnsupported.
	Search(ctx context.Context, query string) ([]Book, error)

	// Book отдаёт карточку книги вместе со списком переводов.
	Book(ctx context.Context, bookID string) (*Book, error)

	// Chapters отдаёт главы выбранного перевода в порядке чтения.
	Chapters(ctx context.Context, bookID, editionID string) ([]ChapterInfo, error)

	// Chapter качает одну главу вместе с текстом.
	Chapter(ctx context.Context, bookID, editionID string, ci ChapterInfo) (*Chapter, error)

	// DecodeChapter восстанавливает главу из сохранённого Chapter.Raw.
	DecodeChapter(raw []byte) (*Chapter, error)

	// Fetch качает файл по ссылке из разметки: обложку или иллюстрацию.
	// Относительные адреса источник достраивает сам.
	Fetch(ctx context.Context, rawURL string) (data []byte, contentType string, err error)
}

// Registry — набор подключённых источников.
// Нулевое значение готово к работе; безопасен для одновременного доступа.
type Registry struct {
	mu      sync.RWMutex
	sources map[string]Source
	order   []string
}

// Register добавляет источник. Повторная регистрация того же ID заменяет прежний.
func (r *Registry) Register(s Source) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sources == nil {
		r.sources = map[string]Source{}
	}
	if _, exists := r.sources[s.ID()]; !exists {
		r.order = append(r.order, s.ID())
	}
	r.sources[s.ID()] = s
}

// Get возвращает источник по идентификатору.
func (r *Registry) Get(id string) (Source, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.sources[id]
	return s, ok
}

// For подбирает источник под ссылку.
func (r *Registry) For(rawURL string) (Source, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, id := range r.order {
		if s := r.sources[id]; s.Supports(rawURL) {
			return s, true
		}
	}
	return nil, false
}

// Sources перечисляет подключённые источники в порядке регистрации.
func (r *Registry) Sources() []Source {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Source, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, r.sources[id])
	}
	return out
}

// Resolve разбирает ссылку: находит источник и достаёт из неё идентификатор книги.
func (r *Registry) Resolve(rawURL string) (Source, string, error) {
	s, ok := r.For(rawURL)
	if !ok {
		return nil, "", fmt.Errorf("%w: %s", ErrUnsupported, rawURL)
	}
	id, ok := s.ParseRef(rawURL)
	if !ok {
		return nil, "", fmt.Errorf("%w: не разобрать ссылку %s", ErrUnsupported, rawURL)
	}
	return s, id, nil
}

// SearchAll спрашивает все источники разом и складывает находки вместе.
// Источники без поиска пропускаются; ошибка возвращается, только если
// не ответил вообще никто.
func (r *Registry) SearchAll(ctx context.Context, query string) (map[string][]Book, error) {
	sources := r.Sources()

	var (
		mu      sync.Mutex
		out     = make(map[string][]Book, len(sources))
		errs    []error
		wg      sync.WaitGroup
		anyGood bool
	)
	for _, s := range sources {
		wg.Add(1)
		go func(s Source) {
			defer wg.Done()
			books, err := s.Search(ctx, query)

			mu.Lock()
			defer mu.Unlock()
			switch {
			case errors.Is(err, ErrUnsupported):
				return
			case err != nil:
				errs = append(errs, fmt.Errorf("%s: %w", s.ID(), err))
			default:
				anyGood = true
				if len(books) > 0 {
					out[s.ID()] = books
				}
			}
		}(s)
	}
	wg.Wait()

	if !anyGood && len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return out, nil
}

// SortEditions ставит впереди переводы с наибольшим числом глав —
// это почти всегда то, что нужно предложить первым.
func SortEditions(list []Edition) {
	sort.SliceStable(list, func(i, j int) bool { return list[i].Chapters > list[j].Chapters })
}

// SortChapters приводит главы к порядку чтения.
func SortChapters(list []ChapterInfo) {
	sort.SliceStable(list, func(i, j int) bool { return list[i].Index < list[j].Index })
}

// NormalizeURL приводит ссылку к виду, удобному для сравнения хостов.
func NormalizeURL(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	return strings.TrimPrefix(s, "www.")
}
