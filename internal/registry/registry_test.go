package registry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rossedman/tfmetrics/internal/ghapi"

	"github.com/rossedman/tfmetrics/internal/source"
)

// fakeRegistry serves service discovery and the versions endpoint, counting hits.
type fakeRegistry struct {
	*httptest.Server
	discovery atomic.Int32
	versions  atomic.Int32
	mu        sync.Mutex
	auths     []string
}

func newFakeRegistry(t *testing.T, modulesV1 func(base string) string, status int) *fakeRegistry {
	t.Helper()
	f := &fakeRegistry{}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/terraform.json", func(w http.ResponseWriter, r *http.Request) {
		f.discovery.Add(1)
		f.mu.Lock()
		f.auths = append(f.auths, r.Header.Get("Authorization"))
		f.mu.Unlock()
		fmt.Fprintf(w, `{"modules.v1": %q}`, modulesV1("http://"+r.Host))
	})
	mux.HandleFunc("/v1/modules/", func(w http.ResponseWriter, r *http.Request) {
		f.versions.Add(1)
		f.mu.Lock()
		f.auths = append(f.auths, r.Header.Get("Authorization"))
		f.mu.Unlock()
		if status != 0 && status != http.StatusOK {
			http.Error(w, "nope", status)
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/versions") {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"modules":[{"versions":[{"version":"1.0.0"},{"version":"1.1.0"},{"version":"2.0.0"}]}]}`)
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func (f *fakeRegistry) host() string { return strings.TrimPrefix(f.URL, "http://") }

func newResolver() *Resolver {
	r := New()
	r.Scheme = "http"
	// Retries are the transport's business (tested in ghapi); here one
	// request per lookup keeps hit counts exact.
	r.HTTP.Transport.(*ghapi.Transport).MaxRetries = -1
	return r
}

func TestNewRetriesServerErrors(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"modules":[{"versions":[{"version":"1.0.0"}]}]}`))
	}))
	t.Cleanup(srv.Close)
	r := New()
	r.Scheme = "http"
	r.HTTP.Transport.(*ghapi.Transport).Sleep = func(context.Context, time.Duration) error { return nil }
	r.services = map[string]string{"h": srv.URL + "/"}
	got, err := r.Versions(context.Background(), source.KindRegistry, "h/a/b/c")
	if err != nil || len(got) != 1 || hits.Load() != 2 {
		t.Fatalf("got %v, %v after %d hits; want one retry then success", got, err, hits.Load())
	}
}

func TestNew(t *testing.T) {
	t.Parallel()
	r := New()
	if r.Scheme != "https" || r.HTTP == nil {
		t.Fatalf("New() = %+v", r)
	}
	if (&Resolver{}).scheme() != "https" {
		t.Fatal("zero Resolver should default to https")
	}
}

func TestRegistryVersions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		v1   func(base string) string
	}{
		{"relative with slash", func(string) string { return "/v1/modules/" }},
		{"relative no trailing slash", func(string) string { return "/v1/modules" }},
		{"relative no leading slash", func(string) string { return "v1/modules/" }},
		{"absolute", func(base string) string { return base + "/v1/modules/" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFakeRegistry(t, tt.v1, 0)
			got, err := newResolver().Versions(context.Background(), source.KindRegistry, f.host()+"/ns/vpc/aws")
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"1.0.0", "1.1.0", "2.0.0"}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %v want %v", got, want)
			}
		})
	}
}

func TestRegistryVersionsStripsSubdir(t *testing.T) {
	t.Parallel()
	f := newFakeRegistry(t, func(string) string { return "/v1/modules/" }, 0)
	got, err := newResolver().Versions(context.Background(), source.KindRegistry, f.host()+"/ns/vpc/aws//modules/sub")
	if err != nil || len(got) != 3 {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestPrivateRegistryBearerToken(t *testing.T) {
	t.Parallel()
	f := newFakeRegistry(t, func(string) string { return "/v1/modules/" }, 0)
	r := newResolver()
	r.Tokens = map[string]string{f.host(): "s3cret"}
	if _, err := r.Versions(context.Background(), source.KindPrivateRegistry, f.host()+"/acme/vpc/aws"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.auths) != 2 {
		t.Fatalf("auth headers = %v, want 2 (discovery + versions)", f.auths)
	}
	for _, a := range f.auths {
		if a != "Bearer s3cret" {
			t.Errorf("Authorization = %q", a)
		}
	}
}

func TestNoTokenNoAuthHeader(t *testing.T) {
	t.Parallel()
	f := newFakeRegistry(t, func(string) string { return "/v1/modules/" }, 0)
	r := newResolver()
	r.Tokens = map[string]string{"other.example": "x"}
	if _, err := r.Versions(context.Background(), source.KindRegistry, f.host()+"/a/b/c"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range f.auths {
		if a != "" {
			t.Errorf("unexpected Authorization %q", a)
		}
	}
}

func TestRegistryErrors(t *testing.T) {
	t.Parallel()
	t.Run("versions non-200", func(t *testing.T) {
		t.Parallel()
		f := newFakeRegistry(t, func(string) string { return "/v1/modules/" }, http.StatusForbidden)
		_, err := newResolver().Versions(context.Background(), source.KindRegistry, f.host()+"/a/b/c")
		if err == nil || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "nope") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("discovery non-200", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.NotFoundHandler())
		defer srv.Close()
		_, err := newResolver().Versions(context.Background(), source.KindRegistry, strings.TrimPrefix(srv.URL, "http://")+"/a/b/c")
		if err == nil || !strings.Contains(err.Error(), "service discovery") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("discovery without modules.v1", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"providers.v1":"/x"}`) }))
		defer srv.Close()
		_, err := newResolver().Versions(context.Background(), source.KindRegistry, strings.TrimPrefix(srv.URL, "http://")+"/a/b/c")
		if err == nil || !strings.Contains(err.Error(), "no modules.v1") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("bad json", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `not json`) }))
		defer srv.Close()
		_, err := newResolver().Versions(context.Background(), source.KindRegistry, strings.TrimPrefix(srv.URL, "http://")+"/a/b/c")
		if err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("connection refused", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.NotFoundHandler())
		host := strings.TrimPrefix(srv.URL, "http://")
		srv.Close()
		_, err := newResolver().Versions(context.Background(), source.KindRegistry, host+"/a/b/c")
		if err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("bad request URL", func(t *testing.T) {
		t.Parallel()
		_, err := newResolver().Versions(context.Background(), source.KindRegistry, "bad host/a/b/c")
		if err == nil {
			t.Fatal("want error")
		}
	})
}

func TestBadAddressShape(t *testing.T) {
	t.Parallel()
	for _, addr := range []string{"", "ns/name/provider", "a/b/c/d/e", "justone"} {
		_, err := newResolver().Versions(context.Background(), source.KindRegistry, addr)
		if err == nil || !strings.Contains(err.Error(), "want host/namespace/name/provider") {
			t.Errorf("addr %q: err = %v", addr, err)
		}
	}
}

func TestOtherKindsReturnNil(t *testing.T) {
	t.Parallel()
	got, err := newResolver().Versions(context.Background(), source.KindLocal, "./x")
	if got != nil || err != nil {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestCachingPerRun(t *testing.T) {
	t.Parallel()
	f := newFakeRegistry(t, func(string) string { return "/v1/modules/" }, 0)
	r := newResolver()
	ctx := context.Background()
	for range 3 {
		if _, err := r.Versions(ctx, source.KindRegistry, f.host()+"/a/b/c"); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.versions.Load(); got != 1 {
		t.Errorf("versions hits = %d, want 1", got)
	}
	// A second module on the same host reuses service discovery.
	if _, err := r.Versions(ctx, source.KindRegistry, f.host()+"/a/b/d"); err != nil {
		t.Fatal(err)
	}
	if got := f.discovery.Load(); got != 1 {
		t.Errorf("discovery hits = %d, want 1", got)
	}
	if got := f.versions.Load(); got != 2 {
		t.Errorf("versions hits = %d, want 2", got)
	}
	// Same address but different kind is a separate cache entry.
	if _, err := r.Versions(ctx, source.KindPrivateRegistry, f.host()+"/a/b/c"); err != nil {
		t.Fatal(err)
	}
	if got := f.versions.Load(); got != 3 {
		t.Errorf("versions hits = %d, want 3", got)
	}
}

func TestCachesErrors(t *testing.T) {
	t.Parallel()
	f := newFakeRegistry(t, func(string) string { return "/v1/modules/" }, http.StatusInternalServerError)
	r := newResolver()
	for range 2 {
		if _, err := r.Versions(context.Background(), source.KindRegistry, f.host()+"/a/b/c"); err == nil {
			t.Fatal("want error")
		}
	}
	if got := f.versions.Load(); got != 1 {
		t.Errorf("versions hits = %d, want 1", got)
	}
}

func TestCustomHTTPClientNilFallsBack(t *testing.T) {
	t.Parallel()
	f := newFakeRegistry(t, func(string) string { return "/v1/modules/" }, 0)
	r := &Resolver{Scheme: "http"} // nil HTTP client
	if _, err := r.Versions(context.Background(), source.KindRegistry, f.host()+"/a/b/c"); err != nil {
		t.Fatal(err)
	}
}

func TestGitTagsFake(t *testing.T) {
	t.Parallel()
	var gotURL string
	var calls int
	r := &Resolver{GitTags: func(_ context.Context, url string) ([]string, error) {
		gotURL = url
		calls++
		return []string{"v1.0.0", "v1.1.0"}, nil
	}}
	for range 2 {
		got, err := r.Versions(context.Background(), source.KindGit, "github.com/acme/mod//sub")
		if err != nil || !slices.Equal(got, []string{"v1.0.0", "v1.1.0"}) {
			t.Fatalf("got %v, %v", got, err)
		}
	}
	if gotURL != "https://github.com/acme/mod.git" {
		t.Errorf("url = %q", gotURL)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (cached)", calls)
	}
}

func TestGitTagsFakeError(t *testing.T) {
	t.Parallel()
	want := errors.New("boom")
	r := &Resolver{GitTags: func(context.Context, string) ([]string, error) { return nil, want }}
	if _, err := r.Versions(context.Background(), source.KindGit, "github.com/a/b"); !errors.Is(err, want) {
		t.Fatalf("err = %v", err)
	}
}

// gitRepoWithTags creates a repo with lightweight and annotated tags.
func gitRepoWithTags(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte("# x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "init")
	run("tag", "v1.0.0")
	run("tag", "-a", "v1.1.0", "-m", "annotated")
	run("tag", "release/2")
	return dir
}

func TestLsRemoteTagsLocal(t *testing.T) {
	t.Parallel()
	dir := gitRepoWithTags(t)
	r := &Resolver{}
	got, err := r.lsRemoteTags(context.Background(), "file://"+dir)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	want := []string{"release/2", "v1.0.0", "v1.1.0"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestLsRemoteTagsWithTokenAndError(t *testing.T) {
	t.Parallel()
	r := &Resolver{GitToken: "tok", GitTokenHost: "example.invalid"}
	_, err := r.lsRemoteTags(context.Background(), "file://"+filepath.Join(t.TempDir(), "missing"))
	if err == nil || !strings.Contains(err.Error(), "git ls-remote") {
		t.Fatalf("err = %v", err)
	}
}

// TestGitVersionsDefaultsToLsRemote checks the default lister is used and fails
// cleanly on an unreachable host without touching the network.
func TestGitVersionsDefaultsToLsRemote(t *testing.T) {
	t.Parallel()
	r := &Resolver{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Versions(ctx, source.KindGit, "git.invalid/a/b"); err == nil {
		t.Fatal("want error")
	}
}

func TestParseTags(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"simple", "abc\trefs/tags/v1.0.0\n", []string{"v1.0.0"}},
		{"peeled", "abc\trefs/tags/v1.0.0\ndef\trefs/tags/v1.0.0^{}\n", []string{"v1.0.0", "v1.0.0"}},
		{"only peeled", "def\trefs/tags/v2^{}\n", []string{"v2"}},
		{"heads ignored", "abc\trefs/heads/main\nabc\trefs/tags/v1\n", []string{"v1"}},
		{"no tab", "garbage\n", nil},
		{"nested", "abc\trefs/tags/mod/v1.2.3\n", []string{"mod/v1.2.3"}},
		{"empty name", "abc\trefs/tags/\n", nil},
		{"double peel", "abc\trefs/tags/v1^{}^{}\n", []string{"v1"}},
		{"refs prefixed", "abc\trefs/tags/refs/tags/a\n", nil},
		{"no trailing newline", "abc\trefs/tags/v3", []string{"v3"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ParseTags([]byte(tt.in))
			if !slices.Equal(got, tt.want) {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}

func TestAuthEnv(t *testing.T) {
	t.Parallel()
	env := AuthEnv([]string{"HOME=/h"}, "github.com", "tok")
	want := []string{
		"HOME=/h",
		"GIT_CONFIG_KEY_0=http.https://github.com/.extraheader",
		"GIT_CONFIG_VALUE_0=AUTHORIZATION: basic eC1hY2Nlc3MtdG9rZW46dG9r",
		"GIT_CONFIG_COUNT=1",
	}
	if !slices.Equal(env, want) {
		t.Fatalf("got %v want %v", env, want)
	}
	for _, e := range env {
		if strings.Contains(e, "tok") && !strings.HasPrefix(e, "GIT_CONFIG_VALUE_0=") {
			t.Errorf("token leaked in %q", e)
		}
	}
}

func TestAuthEnvKeepsExistingGitConfig(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		base    []string
		wantIdx string
		wantCnt string
	}{
		{"two entries", []string{"GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=a", "GIT_CONFIG_VALUE_0=1", "GIT_CONFIG_KEY_1=b", "GIT_CONFIG_VALUE_1=2"}, "2", "3"},
		{"last count wins", []string{"GIT_CONFIG_COUNT=5", "GIT_CONFIG_COUNT=1"}, "1", "2"},
		{"bogus count", []string{"GIT_CONFIG_COUNT=x"}, "0", "1"},
		{"negative count", []string{"GIT_CONFIG_COUNT=-3"}, "0", "1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := AuthEnv(slices.Clone(tt.base), "ghe.example.com", "tok")
			if !slices.Equal(env[:len(tt.base)], tt.base) {
				t.Errorf("existing entries changed: %v", env)
			}
			added := env[len(tt.base):]
			if !strings.HasPrefix(added[0], "GIT_CONFIG_KEY_"+tt.wantIdx+"=http.https://ghe.example.com/") ||
				!strings.HasPrefix(added[1], "GIT_CONFIG_VALUE_"+tt.wantIdx+"=AUTHORIZATION: basic ") ||
				added[2] != "GIT_CONFIG_COUNT="+tt.wantCnt {
				t.Errorf("added = %v, want index %s and count %s", added, tt.wantIdx, tt.wantCnt)
			}
		})
	}
}

func FuzzParseTags(f *testing.F) {
	f.Add([]byte("abc\trefs/tags/v1.0.0\ndef\trefs/tags/v1.0.0^{}\n"))
	f.Add([]byte("abc\trefs/heads/main\n"))
	f.Add([]byte("\t\n\trefs/tags/\n"))
	f.Add([]byte("x\trefs/tags/refs/tags/a^{}"))
	f.Fuzz(func(t *testing.T, in []byte) {
		for _, tag := range ParseTags(in) {
			if tag == "" {
				t.Fatalf("empty tag from %q", in)
			}
			if strings.HasPrefix(tag, "refs/") || strings.HasSuffix(tag, "^{}") {
				t.Fatalf("tag %q not normalized (input %q)", tag, in)
			}
		}
	})
}

func BenchmarkParseTags(b *testing.B) {
	var sb strings.Builder
	for i := range 500 {
		fmt.Fprintf(&sb, "%040x\trefs/tags/v1.%d.0\n%040x\trefs/tags/v1.%d.0^{}\n", i, i, i+1, i)
	}
	out := []byte(sb.String())
	b.ReportAllocs()
	for b.Loop() {
		if len(ParseTags(out)) != 1000 {
			b.Fatal("unexpected tag count")
		}
	}
}
