package scan

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rossedman/tfmetrics/internal/classify"
	"github.com/rossedman/tfmetrics/internal/facts"
)

func gitOK(t testing.TB) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

func git(t testing.TB, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func copyTree(t testing.TB, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func fixtureCopy(t testing.TB) string {
	t.Helper()
	dst := t.TempDir()
	copyTree(t, filepath.Join("..", "..", "testdata", "repos", "acme-infra"), dst)
	return dst
}

func TestRepoFromURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want string
	}{
		{"https://github.com/acme/net.git", "acme/net"},
		{"https://github.com/acme/net", "acme/net"},
		{"https://github.com/acme/net/", "acme/net"},
		{"https://user:tok@github.com/acme/net.git", "acme/net"},
		{"git@github.com:acme/net.git", "acme/net"},
		{"ssh://git@github.com/acme/net.git", "acme/net"},
		{"https://ghe.acme.io/platform/net.git", "platform/net"},
		{"https://gitlab.com/group/sub/net.git", "sub/net"},
		{"git@github.com:net.git", ""},
		{"/some/local/path", ""},
		{"/net", ""},
		{"/srv/git/weird.git", ""},
		{"../up/one", ""},
		{"", ""},
		{"https://github.com/net", ""},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			if got := RepoFromURL(tt.in); got != tt.want {
				t.Errorf("RepoFromURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestGitHelpers(t *testing.T) {
	gitOK(t)
	ctx := context.Background()

	t.Run("not a repo", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "plain")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
		if got := HeadCommit(ctx, dir); got != "" {
			t.Errorf("HeadCommit = %q", got)
		}
		if got := RepoName(ctx, dir); got != "local/plain" {
			t.Errorf("RepoName = %q", got)
		}
	})

	t.Run("repo without origin", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "myrepo")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		git(t, dir, "init", "-q")
		if got := HeadCommit(ctx, dir); got != "" {
			t.Errorf("HeadCommit with no commits = %q, want empty", got)
		}
		if got := RepoName(ctx, dir); got != "local/myrepo" {
			t.Errorf("RepoName = %q, want local/myrepo", got)
		}
	})

	t.Run("repo with origin and commit", func(t *testing.T) {
		dir := t.TempDir()
		git(t, dir, "init", "-q")
		git(t, dir, "remote", "add", "origin", "git@github.com:acme/net.git")
		if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}
		git(t, dir, "add", ".")
		git(t, dir, "commit", "-q", "-m", "init")
		want := git(t, dir, "rev-parse", "HEAD")
		if got := HeadCommit(ctx, dir); got != want || len(got) != 40 {
			t.Errorf("HeadCommit = %q, want %q", got, want)
		}
		if got := RepoName(ctx, dir); got != "acme/net" {
			t.Errorf("RepoName = %q", got)
		}
	})

	t.Run("unusable origin falls back", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "weird")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		git(t, dir, "init", "-q")
		git(t, dir, "remote", "add", "origin", "/srv/git/weird.git")
		if got := RepoName(ctx, dir); got != "local/weird" {
			t.Errorf("RepoName = %q", got)
		}
	})
}

func TestDirFixture(t *testing.T) {
	t.Parallel()
	root := fixtureCopy(t)
	now := time.Date(2026, 3, 4, 5, 6, 7, 999, time.FixedZone("x", 3600))
	res, err := Dir(context.Background(), root, Meta{
		Repo:       "acme/acme-infra",
		Topics:     []string{"terraform"},
		Properties: map[string]string{"tier": "gold"},
		Commit:     "deadbeef",
	}, Options{
		IgnoreDirs:    classify.DefaultIgnoreDirs,
		OwnerProperty: "owner",
		Now:           func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.SchemaVersion != facts.SchemaVersion || res.Repo != "acme/acme-infra" || res.Org != "acme" || res.Commit != "deadbeef" {
		t.Errorf("metadata wrong: %+v", res)
	}
	if want := time.Date(2026, 3, 4, 4, 6, 7, 0, time.UTC); !res.ScannedAt.Equal(want) || res.ScannedAt.Location() != time.UTC {
		t.Errorf("ScannedAt = %v, want %v UTC truncated", res.ScannedAt, want)
	}
	if !reflect.DeepEqual(res.Owners, []string{"acme/platform"}) {
		t.Errorf("Owners = %v", res.Owners)
	}
	if !reflect.DeepEqual(res.Topics, []string{"terraform"}) || res.Properties["tier"] != "gold" {
		t.Errorf("topics/properties: %v %v", res.Topics, res.Properties)
	}
	var paths []string
	kinds := map[string]string{}
	for _, m := range res.Modules {
		paths = append(paths, m.Path)
		kinds[m.Path] = m.Kind
	}
	wantPaths := []string{"broken", "live/prod", "live/staging", "modules/legacy", "modules/vpc"}
	if !reflect.DeepEqual(paths, wantPaths) {
		t.Errorf("module paths = %v, want %v", paths, wantPaths)
	}
	if kinds["live/prod"] != "root" || kinds["live/staging"] != "root" || kinds["modules/vpc"] != "child" {
		t.Errorf("kinds = %v", kinds)
	}
}

func TestDirDefaults(t *testing.T) {
	gitOK(t)
	dir := filepath.Join(t.TempDir(), "solo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte("terraform {\n  backend \"local\" {}\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init", "-q")
	git(t, dir, "remote", "add", "origin", "https://github.com/acme/solo.git")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "init")
	head := git(t, dir, "rev-parse", "HEAD")

	before := time.Now().UTC().Add(-2 * time.Second)
	res, err := Dir(context.Background(), dir, Meta{}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Repo != "acme/solo" || res.Org != "acme" || res.Commit != head {
		t.Errorf("derived metadata wrong: repo=%q org=%q commit=%q", res.Repo, res.Org, res.Commit)
	}
	if res.ScannedAt.Before(before) {
		t.Errorf("ScannedAt = %v, want near now", res.ScannedAt)
	}
	if res.Topics == nil || res.Properties == nil || res.Modules == nil {
		t.Errorf("nil collections in %+v", res)
	}
	if !reflect.DeepEqual(res.Owners, []string{"unowned"}) {
		t.Errorf("Owners = %v", res.Owners)
	}
	if len(res.Modules) != 1 || res.Modules[0].Path != "." || res.Modules[0].Kind != "root" || res.Modules[0].Backend != "local" {
		t.Errorf("modules = %+v", res.Modules)
	}
}

func TestDirLocalFallback(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "scratch")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	res, err := Dir(context.Background(), dir, Meta{}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Repo != "local/scratch" || res.Org != "local" || res.Commit != "" {
		t.Errorf("got repo=%q org=%q commit=%q", res.Repo, res.Org, res.Commit)
	}
	if len(res.Modules) != 0 {
		t.Errorf("modules = %v", res.Modules)
	}
}

func TestDirRootOverridesAndIgnores(t *testing.T) {
	t.Parallel()
	root := fixtureCopy(t)
	res, err := Dir(context.Background(), root, Meta{Repo: "acme/x"}, Options{
		IgnoreDirs:    []string{"examples", "broken", "live"},
		RootOverrides: []string{"modules/vpc"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, m := range res.Modules {
		got[m.Path] = m.Kind
	}
	want := map[string]string{"modules/legacy": "child", "modules/vpc": "root"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestDirErrors(t *testing.T) {
	t.Parallel()
	if _, err := Dir(context.Background(), filepath.Join(t.TempDir(), "missing"), Meta{Repo: "a/b", Commit: "c"}, Options{}); err == nil {
		t.Error("missing root: expected error")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Dir(ctx, fixtureCopy(t), Meta{Repo: "a/b", Commit: "c"}, Options{}); err == nil {
		t.Error("cancelled context: expected error")
	}
}

func BenchmarkDirFixture(b *testing.B) {
	root := fixtureCopy(b)
	opts := Options{IgnoreDirs: classify.DefaultIgnoreDirs}
	meta := Meta{Repo: "acme/acme-infra", Commit: "c"}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Dir(context.Background(), root, meta, opts); err != nil {
			b.Fatal(err)
		}
	}
}
