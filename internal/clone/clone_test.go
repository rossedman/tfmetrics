package clone

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// srcRepo is a local repo served over file://.
type srcRepo struct {
	dir  string
	url  string
	head string // SHA of the default branch tip
	v1   string // SHA of the first commit (tagged v1)
	feat string // SHA of branch feature
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newSrcRepo(t *testing.T) srcRepo {
	t.Helper()
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "config", "uploadpack.allowFilter", "true")
	gitIn(t, dir, "config", "uploadpack.allowAnySHA1InWant", "true")
	write(t, dir, "main.tf", "# v1\n")
	write(t, dir, "README.md", "readme\n")
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "one")
	gitIn(t, dir, "tag", "v1")
	v1 := gitIn(t, dir, "rev-parse", "HEAD")

	gitIn(t, dir, "checkout", "-q", "-b", "feature")
	write(t, dir, "feature.tf", "# feature\n")
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "feat")
	feat := gitIn(t, dir, "rev-parse", "HEAD")

	gitIn(t, dir, "checkout", "-q", "main")
	write(t, dir, "main.tf", "# v2\n")
	write(t, dir, "mods/net/vpc.tf.json", "{}\n")
	write(t, dir, "mods/net/old.tofu", "# tofu\n")
	write(t, dir, "mods/net/old.tofu.json", "{}\n")
	write(t, dir, "CODEOWNERS", "* @acme/infra\n")
	write(t, dir, "docs/CODEOWNERS", "* @acme/docs\n")
	write(t, dir, "src/app.go", "package main\n")
	write(t, dir, "mods/net/notes.txt", "notes\n")
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "two")
	head := gitIn(t, dir, "rev-parse", "HEAD")
	return srcRepo{dir: dir, url: "file://" + dir, head: head, v1: v1, feat: feat}
}

func TestRemoteHead(t *testing.T) {
	t.Parallel()
	r := newSrcRepo(t)
	tests := []struct {
		name    string
		ref     string
		want    string
		wantErr string
	}{
		{"HEAD", "", r.head, ""},
		{"branch", "main", r.head, ""},
		{"other branch", "feature", r.feat, ""},
		{"tag", "v1", r.v1, ""},
		{"unknown ref", "nope", "", `ref "nope" not found`},
		{"full SHA passthrough", strings.Repeat("a", 40), strings.Repeat("a", 40), ""},
		{"short SHA is not passthrough", "abc1234", "", "not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Git{}.RemoteHead(context.Background(), r.url, tt.ref)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("got %s want %s", got, tt.want)
			}
		})
	}
}

func TestRemoteHeadBadRemote(t *testing.T) {
	t.Parallel()
	_, err := Git{}.RemoteHead(context.Background(), "file://"+filepath.Join(t.TempDir(), "missing"), "")
	if err == nil || !strings.Contains(err.Error(), "git ls-remote") {
		t.Fatalf("err = %v", err)
	}
}

func checkedOut(t *testing.T, dir string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(files)
	return files
}

func TestClone(t *testing.T) {
	t.Parallel()
	r := newSrcRepo(t)
	allTF := []string{"CODEOWNERS", "docs/CODEOWNERS", "main.tf", "mods/net/old.tofu", "mods/net/old.tofu.json", "mods/net/vpc.tf.json"}
	tests := []struct {
		name      string
		ref       string
		wantSHA   string
		wantFiles []string
		wantMain  string
	}{
		{"default branch", "", r.head, allTF, "# v2\n"},
		{"branch ref", "feature", r.feat, []string{"feature.tf", "main.tf"}, "# v1\n"},
		{"tag ref", "v1", r.v1, []string{"main.tf"}, "# v1\n"},
		{"SHA ref via fetch", r.v1, r.v1, []string{"main.tf"}, "# v1\n"},
		{"SHA of tip", r.head, r.head, allTF, "# v2\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := filepath.Join(t.TempDir(), "work")
			got, err := Git{}.Clone(context.Background(), r.url, tt.ref, dir)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.wantSHA {
				t.Errorf("sha = %s want %s", got, tt.wantSHA)
			}
			if files := checkedOut(t, dir); !slices.Equal(files, tt.wantFiles) {
				t.Errorf("files = %v want %v", files, tt.wantFiles)
			}
			b, err := os.ReadFile(filepath.Join(dir, "main.tf"))
			if err != nil || string(b) != tt.wantMain {
				t.Errorf("main.tf = %q, %v", b, err)
			}
		})
	}
}

func TestCloneSparseExcludesOthers(t *testing.T) {
	t.Parallel()
	r := newSrcRepo(t)
	dir := filepath.Join(t.TempDir(), "w")
	if _, err := (Git{}).Clone(context.Background(), r.url, "", dir); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"README.md", "src/app.go", "mods/net/notes.txt"} {
		if _, err := os.Stat(filepath.Join(dir, p)); !os.IsNotExist(err) {
			t.Errorf("%s was materialized (err=%v)", p, err)
		}
	}
}

func TestCloneErrors(t *testing.T) {
	t.Parallel()
	r := newSrcRepo(t)
	t.Run("missing repo", func(t *testing.T) {
		t.Parallel()
		_, err := Git{}.Clone(context.Background(), "file://"+filepath.Join(t.TempDir(), "missing"), "", filepath.Join(t.TempDir(), "w"))
		if err == nil || !strings.Contains(err.Error(), "git clone") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("unknown branch", func(t *testing.T) {
		t.Parallel()
		_, err := Git{}.Clone(context.Background(), r.url, "nope", filepath.Join(t.TempDir(), "w"))
		if err == nil || !strings.Contains(err.Error(), "git clone") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("unknown SHA", func(t *testing.T) {
		t.Parallel()
		_, err := Git{}.Clone(context.Background(), r.url, strings.Repeat("b", 40), filepath.Join(t.TempDir(), "w"))
		if err == nil || !strings.Contains(err.Error(), "git fetch") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := (Git{}).Clone(ctx, r.url, "", filepath.Join(t.TempDir(), "w")); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("stages after clone", func(t *testing.T) {
		t.Parallel()
		// A repo with no commits clones empty; sparse-checkout/checkout then fail.
		empty := t.TempDir()
		gitIn(t, empty, "init", "-q", "-b", "main")
		_, err := Git{}.Clone(context.Background(), "file://"+empty, "", filepath.Join(t.TempDir(), "w"))
		if err == nil {
			t.Fatal("want error cloning an empty repo")
		}
	})
}

func TestEnv(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		token    string
		url      string
		wantAuth bool
		wantKey  string
	}{
		{"https with token", "tok", "https://github.com/acme/x.git", true, "GIT_CONFIG_KEY_0=http.https://github.com/.extraheader"},
		{"https with port", "tok", "https://ghe.example.com:8443/acme/x.git", true, "GIT_CONFIG_KEY_0=http.https://ghe.example.com:8443/.extraheader"},
		{"https without token", "", "https://github.com/acme/x.git", false, ""},
		{"http never gets token", "tok", "http://github.com/acme/x.git", false, ""},
		{"file never gets token", "tok", "file:///tmp/x", false, ""},
		{"ssh never gets token", "tok", "ssh://git@github.com/acme/x.git", false, ""},
		{"scp-like never gets token", "tok", "git@github.com:acme/x.git", false, ""},
		{"unparseable url", "tok", "https://[::1", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			base := []string{"HOME=/h"}
			env, sends := Git{Token: tt.token}.env(base, tt.url)
			if !slices.Contains(env, "GIT_TERMINAL_PROMPT=0") {
				t.Error("GIT_TERMINAL_PROMPT=0 missing")
			}
			added := env[len(base)+1:] // after the base env and GIT_TERMINAL_PROMPT=0
			if (len(added) > 0) != tt.wantAuth || sends != tt.wantAuth {
				t.Fatalf("added env = %v, sends token = %v, want auth = %v", added, sends, tt.wantAuth)
			}
			if tt.wantAuth {
				if !slices.Contains(added, tt.wantKey) {
					t.Errorf("env missing %q", tt.wantKey)
				}
				for _, e := range added {
					if strings.Contains(e, "=tok") || strings.Contains(e, " tok") {
						t.Errorf("raw token in %q", e)
					}
				}
			}
		})
	}
}

func TestRedact(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, want string }{
		{"https://github.com/a/b.git", "https://github.com/a/b.git"},
		{"https://user:secret@github.com/a/b.git", "https://github.com/a/b.git"},
		{"https://tok@github.com/a/b.git", "https://github.com/a/b.git"},
		{"git@github.com:a/b.git", "git@github.com:a/b.git"},
		{"https://[::1", "https://[::1"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := redact(tt.in); got != tt.want {
			t.Errorf("redact(%q) = %q want %q", tt.in, got, tt.want)
		}
	}
}

func TestIsSHA(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want bool
	}{
		{strings.Repeat("a", 40), true},
		{strings.Repeat("0123456789", 4), true},
		{strings.Repeat("A", 40), false},
		{strings.Repeat("a", 39), false},
		{strings.Repeat("a", 41), false},
		{strings.Repeat("g", 40), false},
		{"", false},
		{"main", false},
	}
	for _, tt := range tests {
		if got := isSHA(tt.in); got != tt.want {
			t.Errorf("isSHA(%q) = %v", tt.in, got)
		}
	}
}

func TestTransient(t *testing.T) {
	t.Parallel()
	for msg, want := range map[string]bool{
		"git clone: exit status 128: fatal: unable to access 'https://github.com/a/b/': The requested URL returned error: 429": true,
		"error: RPC failed; HTTP 502 curl 22":             true,
		"fatal: the remote end hung up unexpectedly":      true,
		"fetch-pack: unexpected disconnect while reading": true,
		"Could not resolve host: github.com":              true,
		"fatal: repository 'x' not found":                 false,
		"fatal: Authentication failed":                    false,
		"Remote branch nope not found in upstream":        false,
	} {
		if got := Transient(errors.New(msg)); got != want {
			t.Errorf("Transient(%q) = %v, want %v", msg, got, want)
		}
	}
	if Transient(nil) {
		t.Error("nil is not transient")
	}
}

func TestRetry(t *testing.T) {
	t.Parallel()
	transient := errors.New("HTTP 503 Service Unavailable")
	permanent := errors.New("repository not found")
	tests := []struct {
		name      string
		retries   int
		errs      []error
		wantCalls int
		wantErr   error
	}{
		{"success first time", 0, []error{nil}, 1, nil},
		{"transient then success", 0, []error{transient, transient, nil}, 3, nil},
		{"gives up after default retries", 0, []error{transient, transient, transient, transient, transient}, DefaultRetries + 1, transient},
		{"permanent is not retried", 0, []error{permanent}, 1, permanent},
		{"negative retries disables", -1, []error{transient, nil}, 1, transient},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var sleeps []time.Duration
			g := Git{Retries: tt.retries, BaseDelay: time.Second, Sleep: func(_ context.Context, d time.Duration) error {
				sleeps = append(sleeps, d)
				return nil
			}}
			calls, befores := 0, 0
			err := g.retry(context.Background(), func() error {
				calls++
				return tt.errs[min(calls-1, len(tt.errs)-1)]
			}, func() { befores++ })
			if !errors.Is(err, tt.wantErr) || calls != tt.wantCalls || befores != calls-1 || len(sleeps) != calls-1 {
				t.Fatalf("err %v calls %d befores %d sleeps %v", err, calls, befores, sleeps)
			}
			for i, d := range sleeps {
				lo, hi := time.Second<<i/2, time.Second<<i
				if d < lo || d > hi {
					t.Errorf("sleep %d = %s, want within [%s, %s]", i, d, lo, hi)
				}
			}
		})
	}
}

func TestRetryStopsOnCancel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	g := Git{Sleep: func(ctx context.Context, _ time.Duration) error { cancel(); return ctx.Err() }}
	calls := 0
	err := g.retry(ctx, func() error { calls++; return errors.New("connection reset") }, nil)
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("err %v calls %d", err, calls)
	}
	if err := sleepCtx(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("sleepCtx = %v", err)
	}
	if err := sleepCtx(context.Background(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
}

func TestEmptyDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write(t, dir, "a.tf", "x")
	write(t, dir, "sub/b.tf", "y")
	emptyDir(dir)
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("entries %v err %v", entries, err)
	}
}

// isolateGit points git at no user or system config and no proxy, so a test
// sees only the GIT_CONFIG_* entries it sets.
func isolateGit(t *testing.T) {
	t.Helper()
	for k, v := range map[string]string{
		"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_SYSTEM": "/dev/null", "GIT_CONFIG_COUNT": "0",
		"GIT_ASKPASS": "", "SSH_ASKPASS": "", "NO_PROXY": "*", "no_proxy": "*",
	} {
		t.Setenv(k, v)
	}
}

func TestCloneWithTokenKeepsGitConfigFromEnv(t *testing.T) {
	src := newSrcRepo(t)
	isolateGit(t)
	// The https URL only resolves through this insteadOf rewrite, so the clone
	// succeeds only if the token's config entry does not replace it.
	const alias = "https://example.invalid/acme/x.git"
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url."+src.url+".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", alias)
	sha, err := Git{Token: "tok", Retries: -1}.Clone(context.Background(), alias, "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if sha != src.head {
		t.Errorf("sha = %s, want %s", sha, src.head)
	}
}

func TestCloneAuthFailureSaysWhy(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	isolateGit(t)
	t.Setenv("GIT_SSL_NO_VERIFY", "true")
	tests := []struct {
		name, token, want string
	}{
		{"token rejected", "tok", "the server rejected the GitHub token"},
		{"no token", "", "set a GitHub token that can read it"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Git{Token: tt.token, Retries: -1}.Clone(context.Background(), srv.URL+"/acme/x.git", "", t.TempDir())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.want)
			}
			if strings.Contains(err.Error(), "tok@") {
				t.Errorf("token leaked: %v", err)
			}
		})
	}
}

func TestAuthFailed(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]bool{
		"fatal: could not read Username for 'https://github.com': terminal prompts disabled":   true,
		"remote: Invalid username or password.\nfatal: Authentication failed for 'https://x/'": true,
		"fatal: unable to access 'https://x/': Could not resolve host: x":                      false,
		"": false,
	} {
		if got := authFailed(in); got != want {
			t.Errorf("authFailed(%q) = %v, want %v", in, got, want)
		}
	}
}
