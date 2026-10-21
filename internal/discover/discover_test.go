package discover

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/rossedman/tfmetrics/internal/config"
)

type ghRepo struct {
	Name          string   `json:"name"`
	FullName      string   `json:"full_name"`
	CloneURL      string   `json:"clone_url"`
	DefaultBranch string   `json:"default_branch"`
	Topics        []string `json:"topics"`
	Archived      bool     `json:"archived"`
	Fork          bool     `json:"fork"`
}

func repo(org, name string, topics []string, archived, fork bool) ghRepo {
	return ghRepo{
		Name: name, FullName: org + "/" + name, CloneURL: "https://github.com/" + org + "/" + name + ".git",
		DefaultBranch: "main", Topics: topics, Archived: archived, Fork: fork,
	}
}

type propVal struct {
	RepositoryName string         `json:"repository_name"`
	Properties     []propNameVals `json:"properties"`
}

type propNameVals struct {
	PropertyName string `json:"property_name"`
	Value        any    `json:"value"`
}

type fakeGH struct {
	repos     []ghRepo // served 2 per page
	props     []propVal
	propsCode int // 0 => 200
	repoCode  int
	srv       *httptest.Server
	hits      atomic.Int32
}

func newFakeGH(t *testing.T, f *fakeGH) *GitHub {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v3/orgs/{org}/repos", func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		if f.repoCode != 0 {
			http.Error(w, `{"message":"boom"}`, f.repoCode)
			return
		}
		page := 1
		if p := r.URL.Query().Get("page"); p != "" {
			_, _ = fmt.Sscan(p, &page)
		}
		const per = 2
		lo := min((page-1)*per, len(f.repos))
		hi := min(lo+per, len(f.repos))
		if hi < len(f.repos) {
			u := *r.URL
			q := u.Query()
			q.Set("page", fmt.Sprint(page+1))
			next := "http://" + r.Host + u.Path + "?" + q.Encode()
			w.Header().Set("Link", fmt.Sprintf(`<%s>; rel="next"`, next))
		}
		_ = json.NewEncoder(w).Encode(f.repos[lo:hi])
	})
	mux.HandleFunc("GET /api/v3/orgs/{org}/properties/values", func(w http.ResponseWriter, r *http.Request) {
		if f.propsCode != 0 {
			http.Error(w, `{"message":"props"}`, f.propsCode)
			return
		}
		page := 1
		if p := r.URL.Query().Get("page"); p != "" {
			_, _ = fmt.Sscan(p, &page)
		}
		const per = 2
		lo := min((page-1)*per, len(f.props))
		hi := min(lo+per, len(f.props))
		if hi < len(f.props) {
			q := r.URL.Query()
			q.Set("page", fmt.Sprint(page+1))
			w.Header().Set("Link", fmt.Sprintf(`<http://%s%s?%s>; rel="next"`, r.Host, r.URL.Path, q.Encode()))
		}
		_ = json.NewEncoder(w).Encode(f.props[lo:hi])
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)

	g, err := NewGitHub("tok", f.srv.URL, 1000)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func names(rs []Repo) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.FullName
	}
	return out
}

func fixtureGH() *fakeGH {
	return &fakeGH{
		repos: []ghRepo{
			repo("acme", "zeta", []string{"terraform", "prod"}, false, false),
			repo("acme", "alpha", []string{"terraform"}, false, false),
			repo("acme", "old", []string{"terraform"}, true, false),
			repo("acme", "forky", []string{"terraform"}, false, true),
			repo("acme", "web", nil, false, false),
		},
		props: []propVal{
			{"zeta", []propNameVals{{"team", "platform"}, {"envs", []any{"dev", "prod"}}, {"empty", nil}}},
			{"alpha", []propNameVals{{"team", "data"}, {"envs", []any{"dev"}}}},
			{"web", []propNameVals{{"team", "frontend"}, {"blank", ""}, {"mixed", []any{"x", "y"}}}},
			{"old", []propNameVals{{"team", "platform"}}},
		},
	}
}

func TestOrgFilters(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		org      config.Org
		defaults config.RepoDefault
		want     []string
	}{
		{"no filters skips archived and forks", config.Org{Name: "acme"}, config.RepoDefault{}, []string{"acme/alpha", "acme/web", "acme/zeta"}},
		{"archived allowed", config.Org{Name: "acme"}, config.RepoDefault{Archived: true}, []string{"acme/alpha", "acme/old", "acme/web", "acme/zeta"}},
		{"forks allowed", config.Org{Name: "acme"}, config.RepoDefault{Forks: true}, []string{"acme/alpha", "acme/forky", "acme/web", "acme/zeta"}},
		{"both allowed", config.Org{Name: "acme"}, config.RepoDefault{Forks: true, Archived: true}, []string{"acme/alpha", "acme/forky", "acme/old", "acme/web", "acme/zeta"}},
		{"name glob", config.Org{Name: "acme", Include: config.Filter{Names: []string{"a*"}}}, config.RepoDefault{}, []string{"acme/alpha"}},
		{"name regex", config.Org{Name: "acme", Include: config.Filter{Names: []string{"re:^(web|zeta)$"}}}, config.RepoDefault{}, []string{"acme/web", "acme/zeta"}},
		{"topic", config.Org{Name: "acme", Include: config.Filter{Topics: []string{"PROD"}}}, config.RepoDefault{}, []string{"acme/zeta"}},
		{"property single", config.Org{Name: "acme", Include: config.Filter{Properties: map[string][]string{"team": {"data"}}}}, config.RepoDefault{}, []string{"acme/alpha"}},
		{"property multi-select any-of", config.Org{Name: "acme", Include: config.Filter{Properties: map[string][]string{"envs": {"prod"}}}}, config.RepoDefault{}, []string{"acme/zeta"}},
		{"property or values", config.Org{Name: "acme", Include: config.Filter{Properties: map[string][]string{"team": {"data", "frontend"}}}}, config.RepoDefault{}, []string{"acme/alpha", "acme/web"}},
		{"kinds are ANDed", config.Org{Name: "acme", Include: config.Filter{Topics: []string{"terraform"}, Properties: map[string][]string{"team": {"platform"}}}}, config.RepoDefault{}, []string{"acme/zeta"}},
		{"exclude wins", config.Org{Name: "acme", Include: config.Filter{Topics: []string{"terraform"}}, Exclude: config.Filter{Names: []string{"zeta"}}}, config.RepoDefault{}, []string{"acme/alpha"}},
		{"exclude by property", config.Org{Name: "acme", Exclude: config.Filter{Properties: map[string][]string{"team": {"platform"}}}}, config.RepoDefault{Archived: true}, []string{"acme/alpha", "acme/web"}},
		{"nothing matches", config.Org{Name: "acme", Include: config.Filter{Names: []string{"nope"}}}, config.RepoDefault{}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			g := newFakeGH(t, fixtureGH())
			got, err := g.Org(context.Background(), tt.org, tt.defaults)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(names(got), tt.want) && (len(got) != 0 || len(tt.want) != 0) {
				t.Fatalf("got %v want %v", names(got), tt.want)
			}
		})
	}
}

func TestOrgFields(t *testing.T) {
	t.Parallel()
	f := fixtureGH()
	g := newFakeGH(t, f)
	got, err := g.Org(context.Background(), config.Org{Name: "acme"}, config.RepoDefault{})
	if err != nil {
		t.Fatal(err)
	}
	if f.hits.Load() < 2 {
		t.Errorf("repo list hit %d times, want pagination across pages", f.hits.Load())
	}
	byName := map[string]Repo{}
	for _, r := range got {
		byName[r.Name] = r
	}
	z := byName["zeta"]
	if z.Org != "acme" || z.FullName != "acme/zeta" || z.CloneURL != "https://github.com/acme/zeta.git" || z.DefaultBranch != "main" || z.LocalPath != "" {
		t.Errorf("zeta = %+v", z)
	}
	wantProps := map[string]string{"team": "platform", "envs": "dev,prod"}
	if !reflect.DeepEqual(z.Properties, wantProps) {
		t.Errorf("zeta props = %v want %v (null dropped, multi-select flattened)", z.Properties, wantProps)
	}
	if !reflect.DeepEqual(z.Topics, []string{"terraform", "prod"}) {
		t.Errorf("zeta topics = %v", z.Topics)
	}
	w := byName["web"]
	if w.Topics == nil || len(w.Topics) != 0 {
		t.Errorf("web topics = %#v, want empty non-nil", w.Topics)
	}
	if !reflect.DeepEqual(w.Properties, map[string]string{"team": "frontend", "mixed": "x,y"}) {
		t.Errorf("web props = %v", w.Properties)
	}
	b, _ := json.Marshal(w)
	if !strings.Contains(string(b), `"topics":[]`) {
		t.Errorf("web JSON = %s", b)
	}
}

func TestOrgSorted(t *testing.T) {
	t.Parallel()
	f := &fakeGH{repos: []ghRepo{
		repo("acme", "c", nil, false, false), repo("acme", "a", nil, false, false),
		repo("acme", "d", nil, false, false), repo("acme", "b", nil, false, false),
	}}
	g := newFakeGH(t, f)
	got, err := g.Org(context.Background(), config.Org{Name: "acme"}, config.RepoDefault{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(names(got), []string{"acme/a", "acme/b", "acme/c", "acme/d"}) {
		t.Fatalf("got %v", names(got))
	}
}

func TestOrgPropertiesStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		code    int
		wantErr bool
	}{
		{"404 means no properties", http.StatusNotFound, false},
		{"403 means no properties", http.StatusForbidden, false},
		{"500 is an error", http.StatusInternalServerError, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := fixtureGH()
			f.propsCode = tt.code
			g := newFakeGH(t, f)
			got, err := g.Org(context.Background(), config.Org{Name: "acme"}, config.RepoDefault{})
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), "custom property values") {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 3 {
				t.Fatalf("got %v", names(got))
			}
			for _, r := range got {
				if len(r.Properties) != 0 {
					t.Errorf("%s props = %v, want empty", r.Name, r.Properties)
				}
			}
		})
	}
}

func TestOrgRepoListError(t *testing.T) {
	t.Parallel()
	f := fixtureGH()
	f.repoCode = http.StatusInternalServerError
	g := newFakeGH(t, f)
	_, err := g.Org(context.Background(), config.Org{Name: "acme"}, config.RepoDefault{})
	if err == nil || !strings.Contains(err.Error(), "list repos in acme") {
		t.Fatalf("err = %v", err)
	}
}

func TestOrgBadFilter(t *testing.T) {
	t.Parallel()
	g := newFakeGH(t, fixtureGH())
	_, err := g.Org(context.Background(), config.Org{Name: "acme", Include: config.Filter{Names: []string{"re:("}}}, config.RepoDefault{})
	if err == nil {
		t.Fatal("want error for bad regex")
	}
}

func TestPropertyValues(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   any
		want []string
	}{
		{"string", "a", []string{"a"}},
		{"empty string", "", nil},
		{"nil", nil, nil},
		{"[]string", []string{"a", "b"}, []string{"a", "b"}},
		{"[]any", []any{"a", "b"}, []string{"a", "b"}},
		{"[]any with junk", []any{"a", 3, "", nil}, []string{"a"}},
		{"number", 3.0, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := propertyValues(tt.in); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %#v want %#v", got, tt.want)
			}
		})
	}
}

func TestNewGitHub(t *testing.T) {
	t.Parallel()
	g, err := NewGitHub("", "", 0)
	if err != nil || g.Client == nil {
		t.Fatalf("NewGitHub: %v", err)
	}
	g, err = NewGitHub("tok", "https://ghe.example.com", 5)
	if err != nil {
		t.Fatal(err)
	}
	if got := g.Client.BaseURL(); got != "https://ghe.example.com/api/v3/" {
		t.Errorf("BaseURL = %q", got)
	}
}

func TestSendsAuthAndUserAgent(t *testing.T) {
	t.Parallel()
	var auth, ua atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth.Store(r.Header.Get("Authorization"))
		ua.Store(r.Header.Get("User-Agent"))
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()
	g, err := NewGitHub("tok", srv.URL, 100)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Org(context.Background(), config.Org{Name: "acme"}, config.RepoDefault{}); err != nil {
		t.Fatal(err)
	}
	if auth.Load() != "Bearer tok" || ua.Load() != "tfmetrics" {
		t.Errorf("auth=%v ua=%v", auth.Load(), ua.Load())
	}
}

type countRT struct{ n atomic.Int32 }

func (c *countRT) RoundTrip(*http.Request) (*http.Response, error) {
	c.n.Add(1)
	return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
}

func TestLimitedTransport(t *testing.T) {
	t.Parallel()
	t.Run("spaces requests", func(t *testing.T) {
		t.Parallel()
		base := &countRT{}
		// burst 1, 20/s => requests 2 and 3 wait ~50ms each.
		lt := &limitedTransport{base: base, limiter: rate.NewLimiter(rate.Limit(20), 1)}
		start := time.Now()
		for range 3 {
			req, _ := http.NewRequest(http.MethodGet, "http://x.invalid/", nil)
			if _, err := lt.RoundTrip(req); err != nil {
				t.Fatal(err)
			}
		}
		if el := time.Since(start); el < 80*time.Millisecond {
			t.Errorf("3 requests took %v, want >= ~100ms", el)
		}
		if base.n.Load() != 3 {
			t.Errorf("base calls = %d", base.n.Load())
		}
	})
	t.Run("cancelled context does not reach base", func(t *testing.T) {
		t.Parallel()
		base := &countRT{}
		lt := &limitedTransport{base: base, limiter: rate.NewLimiter(rate.Limit(1), 1)}
		_ = lt.limiter.Allow() // drain the burst
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://x.invalid/", nil)
		if _, err := lt.RoundTrip(req); err == nil {
			t.Fatal("want error")
		}
		if base.n.Load() != 0 {
			t.Errorf("base called %d times", base.n.Load())
		}
	})
}

// makeCheckout creates dir/name with a .git directory (or file).
func makeCheckout(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if out, err := exec.Command("git", "init", "-q", p).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	return p
}

func TestLocalDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, n := range []string{"infra", "app-web", "app-api", "legacy"} {
		makeCheckout(t, dir, n)
	}
	makeCheckout(t, dir, ".hidden")
	if err := os.Mkdir(filepath.Join(dir, "notgit"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// A worktree-style checkout has .git as a file.
	if err := os.Mkdir(filepath.Join(dir, "wt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "wt", ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		inc     config.Filter
		exc     config.Filter
		want    []string
		wantErr bool
	}{
		{"all", config.Filter{}, config.Filter{}, []string{"acme/app-api", "acme/app-web", "acme/infra", "acme/legacy", "acme/wt"}, false},
		{"include glob", config.Filter{Names: []string{"app-*"}}, config.Filter{}, []string{"acme/app-api", "acme/app-web"}, false},
		{"exclude wins", config.Filter{Names: []string{"app-*"}}, config.Filter{Names: []string{"app-web"}}, []string{"acme/app-api"}, false},
		{"topics match nothing locally", config.Filter{Topics: []string{"terraform"}}, config.Filter{}, nil, false},
		{"bad include", config.Filter{Names: []string{"re:("}}, config.Filter{}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := LocalDir(dir, "acme", tt.inc, tt.exc)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v", err)
			}
			g := names(got)
			if len(g) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(g, tt.want) {
				t.Fatalf("got %v want %v", g, tt.want)
			}
		})
	}

	got, _ := LocalDir(dir, "acme", config.Filter{Names: []string{"infra"}}, config.Filter{})
	if len(got) != 1 {
		t.Fatalf("got %v", got)
	}
	r := got[0]
	if r.LocalPath != filepath.Join(dir, "infra") || r.Org != "acme" || r.Name != "infra" || r.CloneURL != "" {
		t.Errorf("repo = %+v", r)
	}
	if r.Topics == nil || r.Properties == nil {
		t.Errorf("topics/properties must be non-nil: %+v", r)
	}
}

func TestLocalDirMissing(t *testing.T) {
	t.Parallel()
	if _, err := LocalDir(filepath.Join(t.TempDir(), "nope"), "acme", config.Filter{}, config.Filter{}); err == nil {
		t.Fatal("want error")
	}
}
