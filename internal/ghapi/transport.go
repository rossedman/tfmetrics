// Package ghapi is an http.RoundTripper that keeps API traffic within GitHub's
// rate limit policy: requests go out one at a time, an exhausted primary limit
// waits for its reset, secondary limits honor Retry-After (or wait at least a
// minute, doubling each time), and server errors and dropped connections are
// retried with exponential backoff and jitter.
//
// See https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api
// and https://docs.github.com/en/rest/using-the-rest-api/best-practices-for-using-the-rest-api.
package ghapi

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Defaults for a zero Transport.
const (
	DefaultMaxRetries     = 6
	DefaultBaseDelay      = time.Second
	DefaultMaxDelay       = 2 * time.Minute
	DefaultMaxWait        = 65 * time.Minute
	DefaultSecondaryDelay = time.Minute
)

// Limit is the last rate limit state seen for one resource (core, graphql, ...).
type Limit struct {
	Limit     int       `json:"limit"`
	Remaining int       `json:"remaining"`
	Reset     time.Time `json:"reset"`
}

// Stats counts what a Transport did.
type Stats struct {
	Requests int              `json:"requests"`
	Retries  int              `json:"retries"`
	Waited   time.Duration    `json:"waited"`
	Limits   map[string]Limit `json:"limits"`
}

// Transport wraps Base with GitHub's rate limit policy. The zero value is
// ready to use.
type Transport struct {
	Base http.RoundTripper
	// Concurrency caps requests in flight. GitHub asks API clients to make
	// requests serially, so 0 means 1. Non-GitHub hosts may raise it.
	Concurrency int
	// MaxRetries caps retries of one request.
	MaxRetries int
	// BaseDelay and MaxDelay bound the exponential backoff for server errors.
	BaseDelay, MaxDelay time.Duration
	// SecondaryDelay is the first wait on a secondary rate limit that gives no
	// Retry-After; it doubles on each further hit.
	SecondaryDelay time.Duration
	// MaxWait is the longest single wait; a longer one fails the request.
	MaxWait time.Duration
	// OnWait is told about every wait; may be nil.
	OnWait func(reason string, d time.Duration)
	// Sleep, Now and Jitter are replaceable for tests.
	Sleep  func(ctx context.Context, d time.Duration) error
	Now    func() time.Time
	Jitter func() float64

	once   sync.Once
	sem    chan struct{}
	mu     sync.Mutex
	stats  Stats
	limits map[string]Limit
}

// init fills defaults.
func (t *Transport) init() {
	t.once.Do(func() {
		if t.Base == nil {
			t.Base = http.DefaultTransport
		}
		n := t.Concurrency
		if n <= 0 {
			n = 1
		}
		t.sem = make(chan struct{}, n)
		if t.MaxRetries == 0 {
			t.MaxRetries = DefaultMaxRetries
		}
		if t.BaseDelay == 0 {
			t.BaseDelay = DefaultBaseDelay
		}
		if t.MaxDelay == 0 {
			t.MaxDelay = DefaultMaxDelay
		}
		if t.SecondaryDelay == 0 {
			t.SecondaryDelay = DefaultSecondaryDelay
		}
		if t.MaxWait == 0 {
			t.MaxWait = DefaultMaxWait
		}
		if t.Sleep == nil {
			t.Sleep = sleep
		}
		if t.Now == nil {
			t.Now = time.Now
		}
		if t.Jitter == nil {
			t.Jitter = rand.Float64
		}
		t.limits = map[string]Limit{}
	})
}

// sleep waits for d or until ctx is done.
func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	tm := time.NewTimer(d)
	defer tm.Stop()
	select {
	case <-tm.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Stats returns a snapshot of the counters and last-seen limits.
func (t *Transport) Stats() Stats {
	t.init()
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.stats
	s.Limits = make(map[string]Limit, len(t.limits))
	for k, v := range t.limits {
		s.Limits[k] = v
	}
	return s
}

// RoundTrip sends req, waiting and retrying as the rate limit policy says.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.init()
	ctx := req.Context()
	select {
	case t.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-t.sem }()

	resource := resourceOf(req)
	for attempt := 0; ; attempt++ {
		if err := t.waitForReset(ctx, resource); err != nil {
			return nil, err
		}
		r, err := rewind(req, attempt)
		if err != nil {
			return nil, err
		}
		resp, err := t.Base.RoundTrip(r)
		t.count(func(s *Stats) { s.Requests++ })
		if err != nil {
			if ctx.Err() != nil || attempt >= t.MaxRetries || !canRetry(req) || permanent(err) {
				return nil, err
			}
			if err := t.wait(ctx, "network error: "+err.Error(), t.backoff(attempt)); err != nil {
				return nil, err
			}
			continue
		}
		t.observe(resource, resp.Header)
		d, reason := t.retryAfter(resp, attempt)
		if reason == "" || attempt >= t.MaxRetries || !canRetry(req) {
			return resp, nil
		}
		if d > t.MaxWait {
			return resp, nil // let the caller see the rate limit error
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		_ = resp.Body.Close()
		if err := t.wait(ctx, reason, d); err != nil {
			return nil, err
		}
	}
}

// count updates the stats under the lock.
func (t *Transport) count(f func(*Stats)) {
	t.mu.Lock()
	f(&t.stats)
	t.mu.Unlock()
}

// wait sleeps for d, recording it as a retry.
func (t *Transport) wait(ctx context.Context, reason string, d time.Duration) error {
	if t.OnWait != nil {
		t.OnWait(reason, d)
	}
	t.count(func(s *Stats) { s.Retries++; s.Waited += d })
	return t.Sleep(ctx, d)
}

// waitForReset sleeps until the reset when the resource's primary limit is
// known to be exhausted, so no request is wasted on a certain 403.
func (t *Transport) waitForReset(ctx context.Context, resource string) error {
	t.mu.Lock()
	l, ok := t.limits[resource]
	t.mu.Unlock()
	if !ok || l.Limit == 0 || l.Remaining > 0 {
		return nil
	}
	d := l.Reset.Sub(t.Now()) + time.Second
	if d <= 0 {
		return nil
	}
	if d > t.MaxWait {
		return fmt.Errorf("github %s rate limit exhausted until %s", resource, l.Reset.Format(time.RFC3339))
	}
	if t.OnWait != nil {
		t.OnWait("primary rate limit ("+resource+") exhausted", d)
	}
	t.count(func(s *Stats) { s.Waited += d })
	if err := t.Sleep(ctx, d); err != nil {
		return err
	}
	t.mu.Lock()
	l.Remaining = l.Limit // assume the window reset; the next response corrects it
	t.limits[resource] = l
	t.mu.Unlock()
	return nil
}

// observe records the rate limit headers of a response.
func (t *Transport) observe(resource string, h http.Header) {
	limit, err1 := strconv.Atoi(h.Get("X-RateLimit-Limit"))
	remaining, err2 := strconv.Atoi(h.Get("X-RateLimit-Remaining"))
	reset, err3 := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return
	}
	if r := h.Get("X-RateLimit-Resource"); r != "" {
		resource = r
	}
	t.mu.Lock()
	t.limits[resource] = Limit{Limit: limit, Remaining: remaining, Reset: time.Unix(reset, 0)}
	t.mu.Unlock()
}

// retryAfter decides whether a response should be retried, how long to wait
// first, and why. An empty reason means do not retry.
func (t *Transport) retryAfter(resp *http.Response, attempt int) (time.Duration, string) {
	switch resp.StatusCode {
	case http.StatusForbidden, http.StatusTooManyRequests:
		if s := resp.Header.Get("Retry-After"); s != "" {
			if secs, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil {
				// Clamp before converting so a huge value cannot overflow into a
				// short wait; anything past MaxWait is refused by the caller.
				secs = min(secs, int64(t.MaxWait/time.Second)+1)
				return max(time.Duration(secs)*time.Second, time.Second), "rate limited (Retry-After)"
			}
		}
		if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			if reset, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
				return max(time.Unix(reset, 0).Sub(t.Now())+time.Second, time.Second), "primary rate limit exhausted"
			}
		}
		if resp.StatusCode == http.StatusTooManyRequests || mentionsRateLimit(resp) {
			return min(t.SecondaryDelay<<min(attempt, 10), t.MaxWait), "secondary rate limit"
		}
		return 0, "" // a plain 403: permissions, not throttling
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return t.backoff(attempt), "server error " + strconv.Itoa(resp.StatusCode)
	}
	return 0, ""
}

// mentionsRateLimit peeks at a 403 body for GitHub's rate limit wording, then
// restores the body for the caller.
func mentionsRateLimit(resp *http.Response) bool {
	if resp.Body == nil {
		return false
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	resp.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(b), resp.Body), resp.Body}
	s := strings.ToLower(string(b))
	return strings.Contains(s, "rate limit") || strings.Contains(s, "abuse")
}

// backoff returns an exponential delay with equal jitter for the attempt.
func (t *Transport) backoff(attempt int) time.Duration {
	d := min(t.BaseDelay<<min(attempt, 20), t.MaxDelay)
	return d/2 + time.Duration(t.Jitter()*float64(d/2))
}

// resourceOf names the rate limit bucket a request draws from.
func resourceOf(req *http.Request) string {
	p := req.URL.Path
	switch {
	case strings.HasSuffix(p, "/graphql"):
		return "graphql"
	case strings.Contains(p, "/search/"):
		return "search"
	}
	return "core"
}

// permanent reports whether a network error cannot succeed on retry: the
// host does not exist, or its certificate is not trusted. Retrying those only
// spends the backoff, which for an unreachable private registry is the whole
// client timeout.
func permanent(err error) bool {
	var dns *net.DNSError
	if errors.As(err, &dns) && dns.IsNotFound {
		return true
	}
	var certInvalid x509.CertificateInvalidError
	var unknownCA x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var verify *tls.CertificateVerificationError
	return errors.As(err, &certInvalid) || errors.As(err, &unknownCA) || errors.As(err, &hostname) || errors.As(err, &verify)
}

// canRetry reports whether a request can be sent again (its body, if any, can
// be rewound).
func canRetry(req *http.Request) bool {
	return req.Body == nil || req.Body == http.NoBody || req.GetBody != nil
}

// rewind returns the request to send for an attempt, with a fresh body.
func rewind(req *http.Request, attempt int) (*http.Request, error) {
	if attempt == 0 || req.Body == nil || req.Body == http.NoBody {
		return req, nil
	}
	if req.GetBody == nil {
		return nil, errors.New("request body cannot be replayed")
	}
	body, err := req.GetBody()
	if err != nil {
		return nil, err
	}
	r := req.Clone(req.Context())
	r.Body = body
	return r, nil
}
