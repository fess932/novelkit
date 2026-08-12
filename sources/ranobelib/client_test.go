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
			t.Errorf("Site-Id was not sent: %q", got)
		}
		if got := r.URL.Query().Get("branch_id"); got != "42" {
			t.Errorf("branch_id was not sent: %q", got)
		}
		w.Write([]byte(`{"data":{"id":7,"volume":"1","number":"2","name":"Prologue",
			"content":{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"text"}]}]},
			"attachments":[{"name":"pic","extension":"jpg","url":"/uploads/pic.jpg"}]}}`))
	}))

	ch, err := c.Chapter(context.Background(), "1--test", ranobelib.ChapterRef{Volume: "1", Number: "2", BranchID: 42})
	if err != nil {
		t.Fatal(err)
	}
	if ch.Title() != "Chapter 2. Prologue" {
		t.Errorf("chapter heading: %q", ch.Title())
	}
	if len(ch.Attachments) != 1 || ch.Attachments[0].URL != "/uploads/pic.jpg" {
		t.Errorf("attachments parsed wrong: %+v", ch.Attachments)
	}
	if body := ch.Content().XHTML(nil); !strings.Contains(body, "text") {
		t.Errorf("chapter content was lost: %q", body)
	}
}

// The blurb arrives as an object one time and a string the next; the client must take both.
func TestMangaSummaryBothShapes(t *testing.T) {
	for _, body := range []string{
		`{"data":{"id":1,"rus_name":"Book","summary":{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"The blurb."}]}]}}}`,
		`{"data":{"id":1,"rus_name":"Book","summary":"<p>The blurb.</p>"}}`,
	} {
		c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(body))
		}))
		m, err := c.Manga(context.Background(), "1--test")
		if err != nil {
			t.Fatal(err)
		}
		if got := m.Summary().PlainText(); got != "The blurb." {
			t.Errorf("the blurb parsed as %q", got)
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
		t.Fatal("expected an error")
	}
	if !errors.Is(err, ranobelib.ErrNotFound) {
		t.Errorf("a 404 must read as ErrNotFound, got %v", err)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("a 404 must not be retried, but there were %d requests", n)
	}
}

// A 429 is retried, honouring Retry-After, and the caller is told about it.
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
		t.Fatalf("the request should have been retried after a 429: %v", err)
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Errorf("expected 2 requests, got %d", n)
	}
	var throttled bool
	for _, n := range notices {
		if n.Kind == "throttle" {
			throttled = true
		}
	}
	if !throttled {
		t.Error("the rate limit was never reported to the caller")
	}
}

func TestRetriesGiveUp(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}), ranobelib.WithRetries(1))

	_, err := c.Chapters(context.Background(), "1--test")
	var apiErr *ranobelib.Error
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected a 500, got %v", err)
	}
	if !apiErr.Retryable {
		t.Error("a 500 must be marked retryable")
	}
}

// The pause between requests is honoured; otherwise the site cuts access quickly.
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
		t.Errorf("pauses were not honoured: three requests took %v", elapsed)
	}
}

func TestContextCancels(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[]}`))
	}), ranobelib.WithThrottle(time.Hour, 0))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	// The first request goes straight out; the second hits the pause and must cancel.
	if _, err := c.Chapters(ctx, "1--test"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Chapters(ctx, "1--test"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected a context cancellation, got %v", err)
	}
}

// An account token travels with every request, including file downloads.
func TestTokenIsSent(t *testing.T) {
	var seen []string
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		w.Write([]byte(`{"data":[]}`))
	}), ranobelib.WithToken("  secret  "))

	if _, err := c.Chapters(context.Background(), "1--test"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Fetch(context.Background(), "/uploads/pic.jpg"); err != nil {
		t.Fatal(err)
	}
	for _, got := range seen {
		if got != "Bearer secret" {
			t.Errorf("Authorization header: %q", got)
		}
	}
	if len(seen) != 2 {
		t.Errorf("expected 2 requests, got %d", len(seen))
	}
}

// Without a token nothing is sent, and a 404 explains that a token might be the
// reason: the site hides restricted titles behind the same status.
func TestNoTokenNoHeaderButAHint(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization sent without a token: %q", got)
		}
		w.WriteHeader(http.StatusNotFound)
	}))

	_, err := c.Manga(context.Background(), "1--test")
	if !errors.Is(err, ranobelib.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if !strings.Contains(err.Error(), "no token") {
		t.Errorf("a 404 without a token should mention it: %v", err)
	}
}
