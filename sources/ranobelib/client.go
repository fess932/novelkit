// Package ranobelib is a client for the public ranobelib.me API and the
// novel.Source implementation built on top of it.
//
// The client paces itself: requests go out strictly one at a time, with a pause
// and a random jitter between them, and the pause grows for a while after a 429
// or 503. That is the only way not to get cut off, so the pacing lives inside
// the client rather than being left to the caller.
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

// Default addresses and site identifier.
const (
	DefaultAPIURL  = "https://api.cdnlibs.org/api"
	DefaultSiteURL = "https://ranobelib.me"
	SiteIDRanobe   = "3"

	defaultUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
		"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)

// ErrNotFound is returned for a 404: no such chapter, a wrong slug, a removed book.
var ErrNotFound = errors.New("ranobelib: not found")

// Error is a failed API call.
type Error struct {
	Op         string // library method, e.g. "Chapter"
	URL        string
	StatusCode int    // 0 when no response arrived
	Message    string // text from the response body, when there was one
	Retryable  bool   // whether retrying makes sense
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

// Is makes errors.Is work with both ErrNotFound and novel.ErrNotFound.
func (e *Error) Is(target error) bool {
	return target == ErrNotFound && e.StatusCode == http.StatusNotFound
}

// Notice tells the caller that the client has slowed down or is retrying.
// Worth showing to a user, so a long pause does not look like a hang.
type Notice struct {
	Kind    string // "throttle", "retry", "network"
	Message string
	Wait    time.Duration
	Attempt int
}

// Client is a concurrency-safe API client. The zero value is not usable; call New.
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

	mu    sync.Mutex // serialises requests: the site must never see parallel calls
	delay time.Duration
	last  time.Time
}

// Option configures a client.
type Option func(*Client)

// WithHTTPClient swaps the http.Client: timeouts, proxies, a test transport.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.httpc = h } }

// WithThrottle sets the pause between requests: base plus a random addition up to jitter.
func WithThrottle(base, jitter time.Duration) Option {
	return func(c *Client) {
		c.baseDelay, c.jitter, c.delay = base, jitter, base
	}
}

// WithRetries sets how many times a network error, 429, 503 or 5xx is retried.
func WithRetries(n int) Option { return func(c *Client) { c.retries = n } }

// WithUserAgent overrides the User-Agent.
func WithUserAgent(ua string) Option { return func(c *Client) { c.ua = ua } }

// WithAPIURL and WithSiteURL are for tests and mirrors.
func WithAPIURL(u string) Option  { return func(c *Client) { c.apiURL = strings.TrimRight(u, "/") } }
func WithSiteURL(u string) Option { return func(c *Client) { c.siteURL = strings.TrimRight(u, "/") } }

// WithSiteID switches the section: "3" for novels, "1" for manga.
func WithSiteID(id string) Option { return func(c *Client) { c.siteID = id } }

// WithNotifier subscribes the caller to pause and retry notices.
func WithNotifier(fn func(Notice)) Option { return func(c *Client) { c.notify = fn } }

// New creates a client with sane pacing: 1.5s plus up to 0.7s of jitter.
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

// SiteURL returns the site address, handy for building a link to a book.
func (c *Client) SiteURL() string { return c.siteURL }

func (c *Client) emit(n Notice) {
	if c.notify != nil {
		c.notify(n)
	}
}

// wait holds the pause before the next request. Called with c.mu held.
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

// slowDown runs after a 429 or 503: the pace drops, and speedUp walks it back
// up again as requests start succeeding.
func (c *Client) slowDown() {
	next := time.Duration(float64(max(c.delay, time.Second)) * 1.7)
	c.delay = min(next, c.maxDelay)
	c.emit(Notice{Kind: "throttle", Message: "rate limited: increased the pause between requests", Wait: c.delay})
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

// get performs a request, honouring the pacing and retries. accept is the wanted response type.
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

// attempt is a single request try. Called with c.mu held.
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
		// A cancelled context is passed through as is; anything else is a retryable network error.
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
			Message: "rate limited",
		}
	case resp.StatusCode >= 500:
		return nil, "", 0, &Error{Op: op, URL: url, StatusCode: resp.StatusCode, Retryable: true}
	case resp.StatusCode >= 400:
		// 4xx is not retried: a paid chapter, a typo in the slug, a removed book.
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
