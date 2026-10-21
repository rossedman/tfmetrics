package ghapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// clock is a fake time source whose Sleep advances it.
type clock struct {
	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration
}

func newClock() *clock { return &clock{now: time.Unix(1_800_000_000, 0)} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sleeps = append(c.sleeps, d)
	c.now = c.now.Add(d)
	return nil
}

func (c *clock) slept() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.sleeps...)
}

// step is one scripted response.
type step struct {
	code    int
	headers map[string]string
	body    string
}

// script serves the steps in order, then 200 OK forever.
func script(t *testing.T, steps ...step) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(n.Add(1)) - 1
		s := step{code: http.StatusOK, body: "ok"}
		if i < len(steps) {
			s = steps[i]
		}
		for k, v := range s.headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(s.code)
		_, _ = io.WriteString(w, s.body)
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

func newTransport(c *clock) *Transport {
	return &Transport{Sleep: c.Sleep, Now: c.Now, Jitter: func() float64 { return 0.5 }}
}

func get(t *testing.T, tr *Transport, url string) (*http.Response, string) {
	t.Helper()
	resp, err := (&http.Client{Transport: tr}).Get(url)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, string(b)
}

func TestRetryPolicy(t *testing.T) {
	t.Parallel()
	reset := strconv.FormatInt(time.Unix(1_800_000_000, 0).Add(90*time.Second).Unix(), 10)
	tests := []struct {
		name      string
		steps     []step
		wantCode  int
		wantHits  int32
		wantSleep []time.Duration
	}{
		{"ok", nil, 200, 1, nil},
		{"retry-after on 429", []step{{code: 429, headers: map[string]string{"Retry-After": "7"}}}, 200, 2, []time.Duration{7 * time.Second}},
		{"retry-after on 403", []step{{code: 403, headers: map[string]string{"Retry-After": "0"}}}, 200, 2, []time.Duration{time.Second}},
		{"primary limit waits for reset", []step{{code: 403, headers: map[string]string{
			"X-RateLimit-Limit": "5000", "X-RateLimit-Remaining": "0", "X-RateLimit-Reset": reset,
		}}}, 200, 2, []time.Duration{91 * time.Second}},
		{"secondary limit without headers doubles from a minute", []step{
			{code: 403, body: `{"message":"You have exceeded a secondary rate limit"}`},
			{code: 429},
		}, 200, 3, []time.Duration{time.Minute, 2 * time.Minute}},
		{"plain 403 is not retried", []step{{code: 403, body: `{"message":"Resource not accessible"}`}}, 403, 1, nil},
		{"404 is not retried", []step{{code: 404}}, 404, 1, nil},
		{"server errors back off exponentially", []step{{code: 502}, {code: 503}, {code: 500}, {code: 504}}, 200, 5,
			[]time.Duration{750 * time.Millisecond, 1500 * time.Millisecond, 3 * time.Second, 6 * time.Second}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv, hits := script(t, tt.steps...)
			c := newClock()
			tr := newTransport(c)
			resp, body := get(t, tr, srv.URL+"/repos")
			if resp.StatusCode != tt.wantCode || hits.Load() != tt.wantHits {
				t.Fatalf("code %d after %d hits, want %d after %d", resp.StatusCode, hits.Load(), tt.wantCode, tt.wantHits)
			}
			if tt.wantCode == 403 && !strings.Contains(body, "not accessible") {
				t.Errorf("403 body lost: %q", body)
			}
			if got := c.slept(); !equalDurations(got, tt.wantSleep) {
				t.Errorf("sleeps = %v, want %v", got, tt.wantSleep)
			}
			if st := tr.Stats(); st.Requests != int(tt.wantHits) || st.Retries != len(tt.wantSleep) {
				t.Errorf("stats = %+v", st)
			}
		})
	}
}

func equalDurations(a, b []time.Duration) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestGivesUpAfterMaxRetries(t *testing.T) {
	t.Parallel()
	srv, hits := script(t, step{code: 503}, step{code: 503}, step{code: 503}, step{code: 503})
	tr := newTransport(newClock())
	tr.MaxRetries = 2
	resp, _ := get(t, tr, srv.URL)
	if resp.StatusCode != 503 || hits.Load() != 3 {
		t.Fatalf("code %d after %d hits, want 503 after 3", resp.StatusCode, hits.Load())
	}
}

func TestNegativeMaxRetriesDisablesRetries(t *testing.T) {
	t.Parallel()
	srv, hits := script(t, step{code: 503})
	tr := newTransport(newClock())
	tr.MaxRetries = -1
	resp, _ := get(t, tr, srv.URL)
	if resp.StatusCode != 503 || hits.Load() != 1 {
		t.Fatalf("code %d after %d hits", resp.StatusCode, hits.Load())
	}
}

func TestWaitLongerThanMaxWaitReturnsResponse(t *testing.T) {
	t.Parallel()
	srv, hits := script(t, step{code: 429, headers: map[string]string{"Retry-After": "7200"}})
	tr := newTransport(newClock())
	resp, _ := get(t, tr, srv.URL)
	if resp.StatusCode != 429 || hits.Load() != 1 {
		t.Fatalf("code %d after %d hits, want the 429 back at once", resp.StatusCode, hits.Load())
	}
}

func TestProactiveWaitForExhaustedLimit(t *testing.T) {
	t.Parallel()
	c := newClock()
	reset := c.Now().Add(30 * time.Second).Unix()
	srv, hits := script(t, step{code: 200, headers: map[string]string{
		"X-RateLimit-Limit": "5000", "X-RateLimit-Remaining": "0", "X-RateLimit-Reset": strconv.FormatInt(reset, 10), "X-RateLimit-Resource": "core",
	}})
	tr := newTransport(c)
	var waits []string
	tr.OnWait = func(reason string, _ time.Duration) { waits = append(waits, reason) }
	get(t, tr, srv.URL+"/a")
	if st := tr.Stats(); st.Limits["core"].Remaining != 0 || st.Limits["core"].Limit != 5000 {
		t.Fatalf("limits = %+v", st.Limits)
	}
	get(t, tr, srv.URL+"/b") // must wait for the reset before sending
	if got := c.slept(); len(got) != 1 || got[0] != 31*time.Second {
		t.Fatalf("sleeps = %v, want one 31s wait", got)
	}
	if hits.Load() != 2 || len(waits) != 1 || !strings.Contains(waits[0], "core") {
		t.Fatalf("hits %d, waits %v", hits.Load(), waits)
	}
	// A different resource has its own budget and is not held up.
	get(t, tr, srv.URL+"/api/graphql")
	if len(c.slept()) != 1 {
		t.Errorf("graphql request waited on the core limit")
	}
}

func TestProactiveWaitTooLongFails(t *testing.T) {
	t.Parallel()
	c := newClock()
	reset := c.Now().Add(3 * time.Hour).Unix()
	srv, _ := script(t, step{code: 200, headers: map[string]string{
		"X-RateLimit-Limit": "60", "X-RateLimit-Remaining": "0", "X-RateLimit-Reset": strconv.FormatInt(reset, 10),
	}})
	tr := newTransport(c)
	get(t, tr, srv.URL)
	if _, err := (&http.Client{Transport: tr}).Get(srv.URL); err == nil || !strings.Contains(err.Error(), "exhausted") {
		t.Fatalf("err = %v, want exhausted error", err)
	}
}

func TestResourceOf(t *testing.T) {
	t.Parallel()
	for path, want := range map[string]string{
		"/repos/a/b":                  "core",
		"/graphql":                    "graphql",
		"/api/graphql":                "graphql",
		"/search/code":                "search",
		"/api/v3/search/repositories": "search",
	} {
		req := httptest.NewRequest(http.MethodGet, "http://x"+path, nil)
		if got := resourceOf(req); got != want {
			t.Errorf("resourceOf(%s) = %s, want %s", path, got, want)
		}
	}
}

func TestPostBodyIsReplayed(t *testing.T) {
	t.Parallel()
	var bodies []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		n := len(bodies)
		mu.Unlock()
		if n == 1 {
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	t.Cleanup(srv.Close)
	tr := newTransport(newClock())
	resp, err := (&http.Client{Transport: tr}).Post(srv.URL+"/graphql", "application/json", strings.NewReader(`{"query":"{viewer{login}}"}`))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("resp %v err %v", resp, err)
	}
	if len(bodies) != 2 || bodies[0] != bodies[1] || bodies[1] == "" {
		t.Fatalf("bodies = %q, want the same body twice", bodies)
	}
}

func TestUnreplayableBodyIsNotRetried(t *testing.T) {
	t.Parallel()
	srv, hits := script(t, step{code: 502})
	tr := newTransport(newClock())
	req, _ := http.NewRequest(http.MethodPost, srv.URL, io.NopCloser(strings.NewReader("x")))
	req.GetBody = nil
	resp, err := tr.RoundTrip(req)
	if err != nil || resp.StatusCode != 502 || hits.Load() != 1 {
		t.Fatalf("resp %v err %v hits %d", resp, err, hits.Load())
	}
	if _, err := rewind(req, 1); err == nil {
		t.Error("rewind of an unreplayable body should fail")
	}
}

// failingRT fails the first n round trips with a network error.
type failingRT struct {
	n    atomic.Int32
	fail int32
}

func (f *failingRT) RoundTrip(*http.Request) (*http.Response, error) {
	if f.n.Add(1) <= f.fail {
		return nil, errors.New("connection reset by peer")
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok")), Header: http.Header{}}, nil
}

func TestNetworkErrorsAreRetried(t *testing.T) {
	t.Parallel()
	c := newClock()
	tr := newTransport(c)
	tr.Base = &failingRT{fail: 2}
	resp, err := (&http.Client{Transport: tr}).Get("http://example.invalid/")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("resp %v err %v", resp, err)
	}
	if len(c.slept()) != 2 {
		t.Errorf("sleeps = %v", c.slept())
	}
	tr2 := newTransport(newClock())
	tr2.Base = &failingRT{fail: 100}
	tr2.MaxRetries = 1
	if _, err := (&http.Client{Transport: tr2}).Get("http://example.invalid/"); err == nil {
		t.Error("want error after retries run out")
	}
}

func TestContextCancelStopsWaiting(t *testing.T) {
	t.Parallel()
	srv, _ := script(t, step{code: 503})
	ctx, cancel := context.WithCancel(context.Background())
	tr := &Transport{Sleep: func(ctx context.Context, _ time.Duration) error { cancel(); return ctx.Err() }}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if _, err := tr.RoundTrip(req); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if _, err := tr.RoundTrip(req); !errors.Is(err, context.Canceled) {
		t.Fatalf("second err = %v, want context.Canceled before sending", err)
	}
}

func TestRequestsAreSerialByDefault(t *testing.T) {
	t.Parallel()
	var inFlight, peak atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := inFlight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(25 * time.Millisecond)
		inFlight.Add(-1)
	}))
	t.Cleanup(srv.Close)
	for _, conc := range []int{0, 4} {
		peak.Store(0)
		tr := &Transport{Concurrency: conc}
		var wg sync.WaitGroup
		for range 16 {
			wg.Go(func() { get(t, tr, srv.URL) })
		}
		wg.Wait()
		want := int32(max(conc, 1))
		if p := peak.Load(); p > want || (conc > 1 && p < 2) {
			t.Errorf("concurrency %d: peak %d in flight", conc, p)
		}
	}
}

func TestRealSleep(t *testing.T) {
	t.Parallel()
	if err := sleep(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if err := sleep(context.Background(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func BenchmarkRoundTrip(b *testing.B) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "4999")
		w.Header().Set("X-RateLimit-Reset", "1800000000")
	}))
	b.Cleanup(srv.Close)
	client := &http.Client{Transport: &Transport{}}
	b.ReportAllocs()
	for b.Loop() {
		resp, err := client.Get(srv.URL)
		if err != nil {
			b.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
}
