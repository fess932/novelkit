package ranobelib_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fess932/novelkit/sources/ranobelib"
)

func testClient(t *testing.T, h http.Handler, opts ...ranobelib.Option) *ranobelib.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	base := []ranobelib.Option{
		ranobelib.WithAPIURL(srv.URL),
		ranobelib.WithSiteURL(srv.URL),
		ranobelib.WithThrottle(0, 0),
	}
	return ranobelib.New(append(base, opts...)...)
}

func TestChapterDecoding(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Site-Id"); got != "3" {
			t.Errorf("не передан Site-Id: %q", got)
		}
		if got := r.URL.Query().Get("branch_id"); got != "42" {
			t.Errorf("не передан branch_id: %q", got)
		}
		w.Write([]byte(`{"data":{"id":7,"volume":"1","number":"2","name":"Пролог",
			"content":{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"текст"}]}]},
			"attachments":[{"name":"pic","extension":"jpg","url":"/uploads/pic.jpg"}]}}`))
	}))

	ch, err := c.Chapter(context.Background(), "1--test", ranobelib.ChapterRef{Volume: "1", Number: "2", BranchID: 42})
	if err != nil {
		t.Fatal(err)
	}
	if ch.Title() != "Глава 2. Пролог" {
		t.Errorf("заголовок главы: %q", ch.Title())
	}
	if len(ch.Attachments) != 1 || ch.Attachments[0].URL != "/uploads/pic.jpg" {
		t.Errorf("вложения разобраны неверно: %+v", ch.Attachments)
	}
	if body := ch.Content().XHTML(nil); !strings.Contains(body, "текст") {
		t.Errorf("содержимое главы потеряно: %q", body)
	}
}

// Аннотация приходит то объектом, то строкой — клиент должен переваривать оба вида.
func TestMangaSummaryBothShapes(t *testing.T) {
	for _, body := range []string{
		`{"data":{"id":1,"rus_name":"Книга","summary":{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"Аннотация."}]}]}}}`,
		`{"data":{"id":1,"rus_name":"Книга","summary":"<p>Аннотация.</p>"}}`,
	} {
		c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(body))
		}))
		m, err := c.Manga(context.Background(), "1--test")
		if err != nil {
			t.Fatal(err)
		}
		if got := m.Summary().PlainText(); got != "Аннотация." {
			t.Errorf("аннотация разобрана как %q", got)
		}
	}
}

func TestNotFoundIsPermanent(t *testing.T) {
	var calls int32
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"data":{"toast":{"message":"Not Found"}}}`))
	}))

	_, err := c.Chapter(context.Background(), "1--test", ranobelib.ChapterRef{Volume: "1", Number: "1"})
	if err == nil {
		t.Fatal("ожидалась ошибка")
	}
	if !errors.Is(err, ranobelib.ErrNotFound) {
		t.Errorf("404 должен распознаваться как ErrNotFound, получено %v", err)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("404 повторять не нужно, а запросов было %d", n)
	}
}

// 429 повторяется с учётом Retry-After, и об этом сообщают наружу.
func TestRetryAfterRateLimit(t *testing.T) {
	var calls int32
	var notices []ranobelib.Notice

	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(`{"data":[]}`))
	}), ranobelib.WithNotifier(func(n ranobelib.Notice) { notices = append(notices, n) }))

	if _, err := c.Chapters(context.Background(), "1--test"); err != nil {
		t.Fatalf("после 429 запрос должен был повториться: %v", err)
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Errorf("ожидалось 2 запроса, было %d", n)
	}
	var throttled bool
	for _, n := range notices {
		if n.Kind == "throttle" {
			throttled = true
		}
	}
	if !throttled {
		t.Error("о срабатывании рейт-лимита не сообщили наружу")
	}
}

func TestRetriesGiveUp(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}), ranobelib.WithRetries(1))

	_, err := c.Chapters(context.Background(), "1--test")
	var apiErr *ranobelib.Error
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("ожидалась ошибка 500, получено %v", err)
	}
	if !apiErr.Retryable {
		t.Error("500 должна помечаться как повторяемая")
	}
}

// Пауза между запросами выдерживается, иначе сайт быстро закроет доступ.
func TestThrottleKeepsPause(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[]}`))
	}), ranobelib.WithThrottle(120*time.Millisecond, 0))

	ctx := context.Background()
	start := time.Now()
	for range 3 {
		if _, err := c.Chapters(ctx, "1--test"); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(start); elapsed < 240*time.Millisecond {
		t.Errorf("паузы между запросами не выдержаны: три запроса заняли %v", elapsed)
	}
}

func TestContextCancels(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[]}`))
	}), ranobelib.WithThrottle(time.Hour, 0))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	// Первый запрос проходит сразу, второй упирается в паузу и должен отмениться.
	if _, err := c.Chapters(ctx, "1--test"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Chapters(ctx, "1--test"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("ожидалась отмена по контексту, получено %v", err)
	}
}
