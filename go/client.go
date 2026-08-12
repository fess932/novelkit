// Package ranobelib — клиент к публичному API ranobelib.me.
//
// Клиент сам держит темп запросов: обращения идут строго последовательно,
// между ними выдерживается пауза со случайным разбросом, а на 429/503
// пауза временно растёт. Это единственный способ не словить бан на сайте,
// поэтому темп зашит в клиент, а не оставлен на совесть вызывающего.
//
//	c := ranobelib.New()
//	m, err := c.Manga(ctx, "14841--beginning-after-the-end-novel")
package ranobelib

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Адреса и идентификатор сайта по умолчанию.
const (
	DefaultAPIURL  = "https://api.cdnlibs.org/api"
	DefaultSiteURL = "https://ranobelib.me"
	SiteIDRanobe   = "3"

	defaultUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
		"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)

// ErrNotFound возвращается для 404: главы нет, слаг неверный, книга удалена.
var ErrNotFound = errors.New("ranobelib: не найдено")

// Error — ошибка обращения к API.
type Error struct {
	Op         string // метод библиотеки, например "Chapter"
	URL        string
	StatusCode int    // 0, если до ответа дело не дошло
	Message    string // текст из тела ответа, если он был
	Retryable  bool   // имеет ли смысл повторить запрос
	Err        error
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("ranobelib: ")
	b.WriteString(e.Op)
	if e.StatusCode != 0 {
		fmt.Fprintf(&b, ": HTTP %d", e.StatusCode)
	}
	if e.Message != "" {
		b.WriteString(": ")
		b.WriteString(e.Message)
	}
	if e.Err != nil {
		b.WriteString(": ")
		b.WriteString(e.Err.Error())
	}
	return b.String()
}

func (e *Error) Unwrap() error { return e.Err }

// Is позволяет писать errors.Is(err, ranobelib.ErrNotFound).
func (e *Error) Is(target error) bool {
	return target == ErrNotFound && e.StatusCode == http.StatusNotFound
}

// Notice — сообщение о том, что клиент притормозил или повторяет запрос.
// Полезно показать пользователю, чтобы долгая пауза не выглядела зависанием.
type Notice struct {
	Kind    string // "throttle", "retry", "network"
	Message string
	Wait    time.Duration
	Attempt int
}

// Client — потокобезопасный клиент API. Нулевое значение непригодно, используйте New.
type Client struct {
	httpc   *http.Client
	apiURL  string
	siteURL string
	ua      string
	siteID  string
	retries int

	baseDelay time.Duration
	jitter    time.Duration
	maxDelay  time.Duration
	notify    func(Notice)

	mu    sync.Mutex // сериализует запросы: параллельных обращений к сайту быть не должно
	delay time.Duration
	last  time.Time
}

// Option настраивает клиент.
type Option func(*Client)

// WithHTTPClient подменяет http.Client (таймауты, прокси, транспорт в тестах).
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.httpc = h } }

// WithThrottle задаёт паузу между запросами: base плюс случайная добавка до jitter.
func WithThrottle(base, jitter time.Duration) Option {
	return func(c *Client) {
		c.baseDelay, c.jitter, c.delay = base, jitter, base
	}
}

// WithRetries задаёт число повторов при сетевой ошибке, 429, 503 и 5xx.
func WithRetries(n int) Option { return func(c *Client) { c.retries = n } }

// WithUserAgent подменяет User-Agent.
func WithUserAgent(ua string) Option { return func(c *Client) { c.ua = ua } }

// WithAPIURL и WithSiteURL нужны для тестов и зеркал.
func WithAPIURL(u string) Option  { return func(c *Client) { c.apiURL = strings.TrimRight(u, "/") } }
func WithSiteURL(u string) Option { return func(c *Client) { c.siteURL = strings.TrimRight(u, "/") } }

// WithSiteID переключает раздел: "3" — ранобэ, "1" — манга.
func WithSiteID(id string) Option { return func(c *Client) { c.siteID = id } }

// WithNotifier подписывает вызывающего на сообщения о паузах и повторах.
func WithNotifier(fn func(Notice)) Option { return func(c *Client) { c.notify = fn } }

// New создаёт клиент с разумными настройками темпа: 1.5 с плюс до 0.7 с разброса.
func New(opts ...Option) *Client {
	c := &Client{
		httpc:     &http.Client{Timeout: 60 * time.Second},
		apiURL:    DefaultAPIURL,
		siteURL:   DefaultSiteURL,
		ua:        defaultUserAgent,
		siteID:    SiteIDRanobe,
		retries:   4,
		baseDelay: 1500 * time.Millisecond,
		jitter:    700 * time.Millisecond,
		maxDelay:  30 * time.Second,
	}
	c.delay = c.baseDelay
	for _, o := range opts {
		o(c)
	}
	return c
}

// SiteURL возвращает адрес сайта: пригодится, чтобы собрать ссылку на книгу.
func (c *Client) SiteURL() string { return c.siteURL }

func (c *Client) emit(n Notice) {
	if c.notify != nil {
		c.notify(n)
	}
}

// wait выдерживает паузу перед очередным запросом. Вызывается под c.mu.
func (c *Client) wait(ctx context.Context) error {
	pause := c.delay
	if c.jitter > 0 {
		pause += rand.N(c.jitter)
	}
	if elapsed := time.Since(c.last); elapsed < pause {
		return sleep(ctx, pause-elapsed)
	}
	return nil
}

// slowDown вызывается после 429/503: темп режется до конца работы клиента,
// speedUp постепенно возвращает его назад после успешных запросов.
func (c *Client) slowDown() {
	next := time.Duration(float64(max(c.delay, time.Second)) * 1.7)
	c.delay = min(next, c.maxDelay)
	c.emit(Notice{Kind: "throttle", Message: "рейт-лимит: увеличена пауза между запросами", Wait: c.delay})
}

func (c *Client) speedUp() {
	if c.delay > c.baseDelay {
		c.delay = max(c.baseDelay, time.Duration(float64(c.delay)*0.85))
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// get выполняет запрос с учётом темпа и повторов. accept — желаемый тип ответа.
func (c *Client) get(ctx context.Context, op, url, accept string) ([]byte, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	var lastErr error
	for attempt := 0; ; attempt++ {
		if err := c.wait(ctx); err != nil {
			return nil, "", err
		}

		body, ctype, retryAfter, err := c.attempt(ctx, op, url, accept)
		if err == nil {
			c.speedUp()
			return body, ctype, nil
		}

		var apiErr *Error
		if errors.As(err, &apiErr) && !apiErr.Retryable {
			return nil, "", err
		}
		lastErr = err
		if attempt >= c.retries {
			return nil, "", lastErr
		}

		wait := retryAfter
		if wait <= 0 {
			wait = min(time.Duration(1<<uint(attempt+1))*time.Second, c.maxDelay)
		}
		c.emit(Notice{Kind: "retry", Message: err.Error(), Wait: wait, Attempt: attempt + 1})
		if err := sleep(ctx, wait); err != nil {
			return nil, "", err
		}
	}
}

// attempt — одна попытка запроса. Вызывается под c.mu.
func (c *Client) attempt(ctx context.Context, op, url, accept string) (body []byte, ctype string, retryAfter time.Duration, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", 0, &Error{Op: op, URL: url, Err: err}
	}
	req.Header.Set("User-Agent", c.ua)
	req.Header.Set("Accept", accept)
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9")
	req.Header.Set("Referer", c.siteURL+"/")
	req.Header.Set("Origin", c.siteURL)
	req.Header.Set("Site-Id", c.siteID)

	c.last = time.Now()
	resp, err := c.httpc.Do(req)
	if err != nil {
		// Отмену контекста наружу отдаём как есть, остальное — повторяемая сетевая ошибка.
		if ctx.Err() != nil {
			return nil, "", 0, ctx.Err()
		}
		return nil, "", 0, &Error{Op: op, URL: url, Retryable: true, Err: err}
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode == http.StatusServiceUnavailable:
		c.slowDown()
		return nil, "", parseRetryAfter(resp.Header.Get("Retry-After")), &Error{
			Op: op, URL: url, StatusCode: resp.StatusCode, Retryable: true,
			Message: "сработал рейт-лимит",
		}
	case resp.StatusCode >= 500:
		return nil, "", 0, &Error{Op: op, URL: url, StatusCode: resp.StatusCode, Retryable: true}
	case resp.StatusCode >= 400:
		// 4xx не повторяем: платная глава, опечатка в слаге, удалённая книга.
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, "", 0, &Error{
			Op: op, URL: url, StatusCode: resp.StatusCode,
			Message: strings.TrimSpace(string(snippet)),
		}
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", 0, &Error{Op: op, URL: url, Retryable: true, Err: err}
	}
	return data, resp.Header.Get("Content-Type"), 0, nil
}

func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}
