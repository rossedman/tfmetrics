package discover

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rossedman/tfmetrics/internal/config"
)

func TestOrgFillsHeadSHAs(t *testing.T) {
	t.Parallel()
	f := fixtureGH()
	f.heads = map[string]string{"zeta": "aaa", "alpha": "bbb"} // web has no default branch
	g := newFakeGH(t, f)
	got, err := g.Org(context.Background(), config.Org{Name: "acme"}, config.RepoDefault{})
	if err != nil {
		t.Fatal(err)
	}
	heads := map[string]string{}
	for _, r := range got {
		heads[r.Name] = r.HeadSHA
	}
	if heads["zeta"] != "aaa" || heads["alpha"] != "bbb" || heads["web"] != "" {
		t.Fatalf("heads = %v", heads)
	}
	if f.gqlHits.Load() != 1 {
		t.Errorf("graphql hits = %d, want 1 batch", f.gqlHits.Load())
	}
}

func TestOrgHeadLookupFailureFallsBack(t *testing.T) {
	t.Parallel()
	f := fixtureGH()
	f.gqlCode = http.StatusBadRequest
	g := newFakeGH(t, f)
	var warnings []string
	g.OnWarning = func(m string) { warnings = append(warnings, m) }
	got, err := g.Org(context.Background(), config.Org{Name: "acme"}, config.RepoDefault{})
	if err != nil {
		t.Fatalf("a failed head lookup must not fail discovery: %v", err)
	}
	if len(got) == 0 || got[0].HeadSHA != "" {
		t.Fatalf("got %+v", got)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "fell back to git ls-remote") {
		t.Errorf("warnings = %v", warnings)
	}
}

// gqlServer serves GraphQL with a handler and counts requests.
func gqlServer(t *testing.T, h func(n int32, query string) any) (*GitHub, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/graphql" || r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "bad request "+r.URL.Path, http.StatusBadRequest)
			return
		}
		var req struct{ Query string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		_ = json.NewEncoder(w).Encode(h(n.Add(1), req.Query))
	}))
	t.Cleanup(srv.Close)
	g, err := NewGitHub("tok", srv.URL, 1000)
	if err != nil {
		t.Fatal(err)
	}
	g.Transport.Sleep = func(context.Context, time.Duration) error { return nil }
	return g, &n
}

func TestHeadSHAsBatches(t *testing.T) {
	t.Parallel()
	g, n := gqlServer(t, func(_ int32, q string) any {
		data := map[string]any{}
		for i := 0; strings.Contains(q, fmt.Sprintf("r%d:", i)); i++ {
			data[fmt.Sprintf("r%d", i)] = map[string]any{
				"name":             fmt.Sprintf("q%d-%d", len(q), i),
				"defaultBranchRef": map[string]any{"target": map[string]any{"oid": "x"}},
			}
		}
		return map[string]any{"data": data}
	})
	names := make([]string, 250)
	for i := range names {
		names[i] = fmt.Sprintf("repo-%d", i)
	}
	heads, err := g.HeadSHAs(context.Background(), "acme", names)
	if err != nil {
		t.Fatal(err)
	}
	if n.Load() != 3 {
		t.Errorf("queries = %d, want 3 for 250 repos", n.Load())
	}
	if len(heads) != 250 {
		t.Errorf("heads = %d, want 250", len(heads))
	}
}

func TestHeadSHAsRateLimitedIsRetried(t *testing.T) {
	t.Parallel()
	g, n := gqlServer(t, func(n int32, _ string) any {
		if n == 1 {
			return map[string]any{"errors": []any{map[string]any{"type": "RATE_LIMITED", "message": "API rate limit exceeded"}}}
		}
		return map[string]any{"data": map[string]any{"r0": map[string]any{
			"name": "a", "defaultBranchRef": map[string]any{"target": map[string]any{"oid": "abc"}},
		}}}
	})
	heads, err := g.HeadSHAs(context.Background(), "acme", []string{"a"})
	if err != nil || heads["a"] != "abc" || n.Load() != 2 {
		t.Fatalf("heads %v err %v after %d queries", heads, err, n.Load())
	}
}

func TestHeadSHAsErrors(t *testing.T) {
	t.Parallel()
	g, _ := gqlServer(t, func(int32, string) any {
		return map[string]any{"errors": []any{map[string]any{"type": "FORBIDDEN", "message": "no access"}}}
	})
	if _, err := g.HeadSHAs(context.Background(), "acme", []string{"a"}); err == nil || !strings.Contains(err.Error(), "no access") {
		t.Fatalf("err = %v", err)
	}
	// A not-found repo next to a found one is a partial success, not an error.
	g2, _ := gqlServer(t, func(int32, string) any {
		return map[string]any{
			"data": map[string]any{"r0": nil, "r1": map[string]any{
				"name": "b", "defaultBranchRef": map[string]any{"target": map[string]any{"oid": "b1"}},
			}},
			"errors": []any{map[string]any{"type": "NOT_FOUND", "message": "Could not resolve to a Repository"}},
		}
	})
	heads, err := g2.HeadSHAs(context.Background(), "acme", []string{"gone", "b"})
	if err != nil || len(heads) != 1 || heads["b"] != "b1" {
		t.Fatalf("heads %v err %v", heads, err)
	}
	// Transport-level failure and an unreachable endpoint.
	g3, err := NewGitHub("tok", "http://127.0.0.1:1", 1000)
	if err != nil {
		t.Fatal(err)
	}
	g3.Transport.MaxRetries = -1
	if _, err := g3.HeadSHAs(context.Background(), "acme", []string{"a"}); err == nil {
		t.Error("want error from unreachable endpoint")
	}
	g3.graphqlURL = "://bad"
	if _, err := g3.HeadSHAs(context.Background(), "acme", []string{"a"}); err == nil {
		t.Error("want error from bad URL")
	}
}

func TestHeadQueryEscapes(t *testing.T) {
	t.Parallel()
	q := headQuery(`ac"me`, []string{"a", `b\c`})
	for _, want := range []string{`r0:repository(owner:"ac\"me",name:"a")`, `r1:repository(owner:"ac\"me",name:"b\\c")`} {
		if !strings.Contains(q, want) {
			t.Errorf("query %s missing %s", q, want)
		}
	}
}

func TestGraphQLURL(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"":                            "https://api.github.com/graphql",
		"https://ghe.acme.io/api/v3/": "https://ghe.acme.io/api/graphql",
		"https://ghe.acme.io/api/v3":  "https://ghe.acme.io/api/graphql",
		"https://ghe.acme.io":         "https://ghe.acme.io/api/graphql",
		"http://127.0.0.1:8080/":      "http://127.0.0.1:8080/api/graphql",
	} {
		if got := GraphQLURL(in); got != want {
			t.Errorf("GraphQLURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func BenchmarkHeadQuery(b *testing.B) {
	names := make([]string, HeadBatch)
	for i := range names {
		names[i] = fmt.Sprintf("terraform-module-%d", i)
	}
	b.ReportAllocs()
	for b.Loop() {
		_ = headQuery("acme", names)
	}
}
