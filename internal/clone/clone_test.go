package clone

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
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
			cmd := exec.Command("git")
			base := cmd.Environ() // the process env may already carry GIT_CONFIG_*
			env := Git{Token: tt.token}.env(cmd, tt.url)
			if !slices.Contains(env, "GIT_TERMINAL_PROMPT=0") {
				t.Error("GIT_TERMINAL_PROMPT=0 missing")
			}
			added := env[len(base)+1:] // after the base env and GIT_TERMINAL_PROMPT=0
			if (len(added) > 0) != tt.wantAuth {
				t.Fatalf("added env = %v, want auth = %v", added, tt.wantAuth)
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
