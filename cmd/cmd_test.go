package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/rossedman/tfmetrics/internal/app"
	"github.com/rossedman/tfmetrics/internal/facts"
)

// fixtureDir is the acme-infra fixture as an absolute path, resolved before
// any test changes directory.
var fixtureDir = func() string {
	p, err := filepath.Abs(filepath.Join("..", "testdata", "repos", "acme-infra"))
	if err != nil {
		panic(err)
	}
	return p
}()

// resetFlags restores every flag on cmd and its subcommands to its default.
func resetFlags(c *cobra.Command) {
	reset := func(f *pflag.Flag) {
		if sv, ok := f.Value.(pflag.SliceValue); ok {
			_ = sv.Replace(nil)
		} else {
			_ = f.Value.Set(f.DefValue)
		}
		f.Changed = false
	}
	c.Flags().VisitAll(reset)
	c.PersistentFlags().VisitAll(reset)
	for _, sub := range c.Commands() {
		resetFlags(sub)
	}
}

// sandbox isolates one test from the real environment: a temp working
// directory, HOME and XDG dirs, no GitHub token and no git config. It returns
// the working directory.
func sandbox(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	work := filepath.Join(dir, "work")
	for _, d := range []string{home, work} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("TFMETRICS_OUT", "")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.com")
	t.Chdir(work)
	reset()
	t.Cleanup(reset)
	return work
}

// reset returns the package globals to a pristine state.
func reset() {
	resetFlags(rootCmd)
	cfgFile = ""
	v = app.NewViper()
	rootCmd.SetArgs(nil)
}

// run executes the root command with args and returns stdout, stderr and the error.
func run(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	reset()
	var out, errb bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errb)
	rootCmd.SetArgs(args)
	err := rootCmd.Execute()
	return out.String(), errb.String(), err
}

// mustRun runs and fails the test on error.
func mustRun(t *testing.T, args ...string) (string, string) {
	t.Helper()
	out, errs, err := run(t, args...)
	if err != nil {
		t.Fatalf("tfmetrics %s: %v\nstdout: %s\nstderr: %s", strings.Join(args, " "), err, out, errs)
	}
	return out, errs
}

func mustContain(t *testing.T, what, got string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("%s missing %q:\n%s", what, w, got)
		}
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// commitDir makes dir a git repo with one commit of its contents.
func commitDir(t *testing.T, dir string) {
	t.Helper()
	git(t, dir, "init", "--quiet", "--initial-branch=main")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "--quiet", "-m", "init")
}

// fixtureCopy copies the acme-infra fixture to parent/name and returns the path.
func fixtureCopy(t *testing.T, parent, name string) string {
	t.Helper()
	dst := filepath.Join(parent, name)
	if err := os.CopyFS(dst, os.DirFS(fixtureDir)); err != nil {
		t.Fatal(err)
	}
	return dst
}

// simpleRepo creates a git repo whose single module makes no remote calls, so
// aggregating it never needs the network.
func simpleRepo(t *testing.T, parent, name string) string {
	t.Helper()
	dir := filepath.Join(parent, name)
	writeFile(t, filepath.Join(dir, "main.tf"), `terraform {
  required_version = ">= 1.5"
}

variable "name" {
  type        = string
  description = "name"
}

resource "aws_s3_bucket" "this" {
  bucket = var.name
}
`)
	commitDir(t, dir)
	return dir
}

// bareRepo returns a file:// URL of a bare clone of a committed repo.
func bareRepo(t *testing.T, src, bare string) string {
	t.Helper()
	cmd := exec.Command("git", "clone", "--quiet", "--bare", src, bare)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git clone --bare: %v\n%s", err, out)
	}
	git(t, bare, "config", "uploadpack.allowFilter", "true")
	git(t, bare, "config", "uploadpack.allowAnySHA1InWant", "true")
	return "file://" + bare
}

func readFacts(t *testing.T, path string) facts.ScanResult {
	t.Helper()
	r, err := facts.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestVersion(t *testing.T) {
	sandbox(t)
	out, _ := mustRun(t, "version")
	mustContain(t, "version output", out, "tfmetrics dev (commit none, built unknown)")
	if _, _, err := run(t, "version", "extra"); err == nil {
		t.Error("version with an argument should fail")
	}
}

func TestRootHelpListsCommands(t *testing.T) {
	sandbox(t)
	out, _ := mustRun(t, "--help")
	mustContain(t, "help", out, "scan", "aggregate", "explain", "org", "version", "--config")
}

func TestScanLocalDir(t *testing.T) {
	work := sandbox(t)
	repo := fixtureCopy(t, work, "acme-infra")
	out, errs := mustRun(t, "scan", repo, "--out", "facts")
	if out != "" {
		t.Errorf("stdout = %q, want empty without --sink", out)
	}
	mustContain(t, "stderr", errs, "scanned local/acme-infra: 5 modules ->", "local__acme-infra.json")
	r := readFacts(t, filepath.Join("facts", "local__acme-infra.json"))
	if r.Repo != "local/acme-infra" || len(r.Modules) != 5 {
		t.Errorf("repo=%s modules=%d", r.Repo, len(r.Modules))
	}
}

func TestScanSinkMarkdown(t *testing.T) {
	work := sandbox(t)
	repo := fixtureCopy(t, work, "acme-infra")
	out, _ := mustRun(t, "scan", repo, "--out", "facts", "--sink", "markdown")
	mustContain(t, "markdown", out, "### tfmetrics: local/acme-infra", "Hygiene score")
}

func TestScanMultipleAndSinkJSON(t *testing.T) {
	work := sandbox(t)
	a := simpleRepo(t, work, "one")
	b := simpleRepo(t, work, "two")
	_, errs := mustRun(t, "scan", a, b, "--out", "facts", "--sink", "json", "--sink", "csv")
	mustContain(t, "stderr", errs, "local/one", "local/two")
	for _, f := range []string{"facts/local__one.json", "facts/local__two.json", "facts/metrics.json", "facts/facts/local__one.json"} {
		if _, err := os.Stat(f); err != nil {
			t.Error(err)
		}
	}
}

func TestScanRemoteFileURL(t *testing.T) {
	work := sandbox(t)
	src := fixtureCopy(t, work, "src")
	commitDir(t, src)
	url := bareRepo(t, src, filepath.Join(work, "remote", "acme", "infra.git"))

	_, errs := mustRun(t, "scan", url, "--out", "facts")
	mustContain(t, "stderr", errs, "scanned acme/infra: 5 modules")
	r := readFacts(t, filepath.Join("facts", "acme__infra.json"))
	if len(r.Commit) != 40 || r.Org != "acme" {
		t.Errorf("commit=%q org=%q", r.Commit, r.Org)
	}

	// --ref picks a branch.
	_, errs = mustRun(t, "scan", url, "--out", "facts2", "--ref", "main")
	mustContain(t, "stderr", errs, "scanned acme/infra")

	// An unreachable ref fails.
	if _, _, err := run(t, "scan", url, "--out", "facts3", "--ref", "no-such-branch"); err == nil {
		t.Error("scan with a missing ref should fail")
	}
}

func TestScanErrors(t *testing.T) {
	work := sandbox(t)
	tests := []struct {
		name       string
		args       []string
		want       string
		wantStderr string
	}{
		{"no args", []string{"scan"}, "requires at least 1 arg", ""},
		{"not a dir or url", []string{"scan", "nonexistent"}, "1 of 1 repos failed to scan", "error: nonexistent: not a directory, URL or owner/name"},
		{"unreachable url", []string{"scan", "file://" + filepath.Join(work, "missing.git")}, "1 of 1 repos failed to scan", "missing.git: git clone: "},
		{"bad sink", []string{"scan", fixtureDir, "--sink", "nope"}, `unknown sink "nope"`, ""},
		{"unknown flag", []string{"scan", ".", "--bogus"}, "unknown flag", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, errs, err := run(t, append(tt.args, "--out", filepath.Join(work, "o"))...)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want containing %q", err, tt.want)
			}
			if !strings.Contains(errs, tt.wantStderr) {
				t.Errorf("stderr = %q, want containing %q", errs, tt.wantStderr)
			}
		})
	}
}

func TestScanContinuesPastFailedRepo(t *testing.T) {
	work := sandbox(t)
	a := simpleRepo(t, work, "one")
	b := simpleRepo(t, work, "two")
	missing := "file://" + filepath.Join(work, "missing.git")
	out, errs, err := run(t, "scan", a, missing, b, "--out", "facts", "--sink", "markdown")
	if err == nil || err.Error() != "1 of 3 repos failed to scan" {
		t.Errorf("err = %v", err)
	}
	mustContain(t, "stderr", errs, "scanned local/one", "error: "+missing+": git clone", "scanned local/two")
	mustContain(t, "markdown", out, "local/one", "local/two")
	for _, f := range []string{"facts/local__one.json", "facts/local__two.json"} {
		if _, err := os.Stat(f); err != nil {
			t.Error(err)
		}
	}
}

// scanFixture scans the fixture into dir/facts and returns that directory.
func scanFixture(t *testing.T, work string) string {
	t.Helper()
	repo := fixtureCopy(t, work, "acme-infra")
	mustRun(t, "scan", repo, "--out", filepath.Join(work, "facts"))
	return filepath.Join(work, "facts")
}

func TestAggregateOfflineSinks(t *testing.T) {
	work := sandbox(t)
	factsDir := scanFixture(t, work)

	tests := []struct {
		sink  string
		files []string
	}{
		{"json", []string{"metrics.json", "facts/local__acme-infra.json"}},
		{"openmetrics", []string{"metrics.prom"}},
		{"csv", []string{"modules.csv", "module_calls.csv"}},
	}
	for _, tt := range tests {
		t.Run(tt.sink, func(t *testing.T) {
			out := filepath.Join(work, "out-"+tt.sink)
			_, errs := mustRun(t, "aggregate", factsDir, "--offline", "--sink", tt.sink, "--out", out)
			mustContain(t, "stderr", errs, "aggregated 1 repos (1 with Terraform) into")
			entries, _ := os.ReadDir(out)
			for _, f := range tt.files {
				if _, err := os.Stat(filepath.Join(out, f)); err != nil {
					var names []string
					for _, e := range entries {
						names = append(names, e.Name())
					}
					t.Errorf("%v (have %v)", err, names)
				}
			}
		})
	}

	t.Run("metrics content", func(t *testing.T) {
		b, err := os.ReadFile(filepath.Join(work, "out-json", "metrics.json"))
		if err != nil {
			t.Fatal(err)
		}
		var m struct {
			Repos  int `json:"repos"`
			Series []struct {
				Name string `json:"name"`
			} `json:"series"`
		}
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		if m.Repos != 1 || len(m.Series) == 0 {
			t.Errorf("metrics = %+v", m)
		}
		for _, s := range m.Series {
			if s.Name == "tf_module_versions_behind" {
				t.Error("versions_behind present with --offline")
			}
		}
		prom, _ := os.ReadFile(filepath.Join(work, "out-openmetrics", "metrics.prom"))
		mustContain(t, "openmetrics", string(prom), "tf_hygiene_score", "tf_repos_total", "# EOF")
	})
}

func TestAggregateDefaultJSONSink(t *testing.T) {
	work := sandbox(t)
	factsDir := scanFixture(t, work)
	mustRun(t, "aggregate", factsDir, "--offline")
	// Default --out is "metrics" under the working directory.
	if _, err := os.Stat(filepath.Join(work, "metrics", "metrics.json")); err != nil {
		t.Error(err)
	}
}

func TestAggregateSinksFromConfigFile(t *testing.T) {
	work := sandbox(t)
	factsDir := scanFixture(t, work)
	prom := filepath.Join(work, "custom", "my.prom")
	cfg := filepath.Join(work, "conf", "tfm.yaml")
	writeFile(t, cfg, fmt.Sprintf(`
labels:
  drop: [resource_type]
hygiene:
  weights:
    pinned: 1
sinks:
  - openmetrics:
      path: %s
  - json:
      path: %s
`, prom, filepath.Join(work, "custom", "json")))

	mustRun(t, "aggregate", factsDir, "--offline", "--config", cfg)
	for _, f := range []string{prom, filepath.Join(work, "custom", "json", "metrics.json")} {
		if _, err := os.Stat(f); err != nil {
			t.Error(err)
		}
	}
	b, _ := os.ReadFile(prom)
	if strings.Contains(string(b), "resource_type") {
		t.Error("labels.drop from config not applied")
	}

	// Naming a sink on the command line takes its options from the config
	// and replaces the configured list.
	if err := os.Remove(prom); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "aggregate", factsDir, "--offline", "--config", cfg, "--sink", "json")
	if _, err := os.Stat(prom); err == nil {
		t.Error("openmetrics sink ran although only --sink json was named")
	}
}

func TestAggregateConfigDiscovery(t *testing.T) {
	work := sandbox(t)
	factsDir := scanFixture(t, work)
	// tfmetrics.yaml in the working directory is picked up without --config.
	writeFile(t, filepath.Join(work, "tfmetrics.yaml"), "sinks:\n  - openmetrics:\n      path: from-cwd.prom\n")
	mustRun(t, "aggregate", factsDir, "--offline")
	if _, err := os.Stat(filepath.Join(work, "from-cwd.prom")); err != nil {
		t.Error(err)
	}
	// So is one under $XDG_CONFIG_HOME/tfmetrics once the cwd file is gone.
	if err := os.Remove(filepath.Join(work, "tfmetrics.yaml")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "tfmetrics", "tfmetrics.yaml"), "sinks:\n  - openmetrics:\n      path: from-xdg.prom\n")
	mustRun(t, "aggregate", factsDir, "--offline")
	if _, err := os.Stat(filepath.Join(work, "from-xdg.prom")); err != nil {
		t.Error(err)
	}
}

func TestAggregateOnlineWithoutRemoteCalls(t *testing.T) {
	work := sandbox(t)
	repo := simpleRepo(t, work, "simple")
	mustRun(t, "scan", repo, "--out", "facts")
	// No remote module calls, so the resolver never needs the network.
	_, errs := mustRun(t, "aggregate", "facts", "--sink", "json", "--out", "m")
	mustContain(t, "stderr", errs, "aggregated 1 repos")
	if strings.Contains(errs, "warning") {
		t.Errorf("unexpected warnings: %s", errs)
	}
}

func TestAggregateErrors(t *testing.T) {
	work := sandbox(t)
	factsDir := scanFixture(t, work)
	writeFile(t, filepath.Join(work, "bad.yaml"), "sinks:\n  - json: {}\n    csv: {}\n")
	writeFile(t, filepath.Join(work, "weights.yaml"), "hygiene:\n  weights:\n    nope: 1\n")
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"no args", []string{"aggregate"}, "requires at least 1 arg"},
		{"missing path", []string{"aggregate", "nope", "--offline"}, "no such file"},
		{"bad sinks shape", []string{"aggregate", factsDir, "--offline", "--config", "bad.yaml"}, "sinks[0]"},
		{"bad weights", []string{"aggregate", factsDir, "--offline", "--config", "weights.yaml"}, `unknown key "nope"`},
		{"unknown sink", []string{"aggregate", factsDir, "--offline", "--sink", "nope"}, `unknown sink "nope"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := run(t, tt.args...)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestExplain(t *testing.T) {
	work := sandbox(t)
	factsDir := scanFixture(t, work)

	out, _ := mustRun(t, "explain", factsDir)
	mustContain(t, "text", out, "local/acme-infra @", "hygiene score", "remote module calls", "diagnostics")

	out, _ = mustRun(t, "explain", factsDir, "--format", "markdown")
	mustContain(t, "markdown", out, "### tfmetrics: local/acme-infra", "**Hygiene score:", "<details>")

	// Custom weights from config flow into the report.
	writeFile(t, filepath.Join(work, "w.yaml"), "hygiene:\n  weights:\n    pinned: 1\n")
	out, _ = mustRun(t, "explain", factsDir, "--config", "w.yaml")
	mustContain(t, "weighted", out, "weight 1\n")

	_, _, err := run(t, "explain", factsDir, "--format", "html")
	if err == nil || !strings.Contains(err.Error(), `unknown format "html"`) {
		t.Errorf("err = %v", err)
	}
	if _, _, err := run(t, "explain", filepath.Join(work, "missing.json")); err == nil {
		t.Error("missing facts file should fail")
	}
	if _, _, err := run(t, "explain"); err == nil {
		t.Error("explain with no args should fail")
	}
}

func TestExplainSeparatesRepos(t *testing.T) {
	work := sandbox(t)
	a := simpleRepo(t, work, "one")
	b := simpleRepo(t, work, "two")
	mustRun(t, "scan", a, b, "--out", "facts")
	out, _ := mustRun(t, "explain", "facts")
	if strings.Count(out, "hygiene score") != 2 {
		t.Errorf("want two reports:\n%s", out)
	}
	idx := strings.Index(out, "local/two")
	if idx < 2 || out[idx-2:idx] != "\n\n" {
		t.Errorf("text reports should be separated by a blank line:\n%s", out)
	}
	out, _ = mustRun(t, "explain", "facts", "--format", "markdown")
	if strings.Count(out, "### tfmetrics:") != 2 {
		t.Errorf("want two markdown reports:\n%s", out)
	}
}

// localClones makes clones/<names...> as committed simple repos.
func localClones(t *testing.T, work string, names ...string) string {
	t.Helper()
	clones := filepath.Join(work, "clones")
	for _, n := range names {
		simpleRepo(t, clones, n)
	}
	return clones
}

func TestOrgDryRunLocal(t *testing.T) {
	work := sandbox(t)
	clones := localClones(t, work, "tf-net", "tf-app", "other")
	writeFile(t, filepath.Join(clones, "not-a-repo", "main.tf"), "")

	out, _ := mustRun(t, "org", "--local-dir", clones, "--org", "acme", "--dry-run")
	mustContain(t, "text", out, "acme/other", "acme/tf-app", "acme/tf-net", "3 repos matched", "topics=")
	if strings.Contains(out, "not-a-repo") {
		t.Errorf("non-git dir listed:\n%s", out)
	}

	out, _ = mustRun(t, "org", "--local-dir", clones, "--org", "acme", "--name", "tf-*", "--dry-run")
	mustContain(t, "filtered", out, "acme/tf-app", "acme/tf-net", "2 repos matched")
	if strings.Contains(out, "other") {
		t.Errorf("name filter not applied:\n%s", out)
	}

	out, _ = mustRun(t, "org", "--local-dir", clones, "--org", "acme", "--name", "tf-*", "--dry-run", "--json")
	var repos []struct {
		Repo      string `json:"repo"`
		Org       string `json:"org"`
		LocalPath string `json:"local_path"`
	}
	if err := json.Unmarshal([]byte(out), &repos); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(repos) != 2 || repos[0].Repo != "acme/tf-app" || repos[0].Org != "acme" || repos[0].LocalPath == "" {
		t.Errorf("repos = %+v", repos)
	}

	// Empty result is [] not null.
	out, _ = mustRun(t, "org", "--local-dir", clones, "--org", "acme", "--name", "zzz", "--dry-run", "--json")
	if strings.TrimSpace(out) != "[]" {
		t.Errorf("empty json = %q", out)
	}
	out, _ = mustRun(t, "org", "--local-dir", clones, "--org", "acme", "--name", "zzz", "--dry-run")
	mustContain(t, "empty", out, "0 repos matched")
}

func TestOrgLocalFullRunAndCache(t *testing.T) {
	work := sandbox(t)
	clones := localClones(t, work, "tf-net", "tf-app")
	cacheDir := filepath.Join(work, "cache-dir")
	args := []string{"org", "--local-dir", clones, "--org", "acme", "--out", "facts", "--cache-dir", cacheDir}

	_, errs := mustRun(t, args...)
	mustContain(t, "first run", errs, "scanned acme/tf-app", "scanned acme/tf-net", "2 repos: 2 scanned, 0 from cache, 0 failed")
	for _, f := range []string{"acme__tf-app.json", "acme__tf-net.json"} {
		r := readFacts(t, filepath.Join("facts", f))
		if r.Org != "acme" || len(r.Commit) != 40 || len(r.Modules) != 1 {
			t.Errorf("%s: org=%q commit=%q modules=%d", f, r.Org, r.Commit, len(r.Modules))
		}
	}

	_, errs = mustRun(t, args...)
	mustContain(t, "second run", errs, "cached  acme/tf-app", "cached  acme/tf-net", "0 scanned, 2 from cache")

	_, errs = mustRun(t, append(args, "--no-cache")...)
	mustContain(t, "no-cache run", errs, "2 scanned, 0 from cache")

	// Changing a scan setting rescans.
	writeFile(t, filepath.Join(work, "c.yaml"), "owners:\n  property: squad\n")
	_, errs = mustRun(t, append(args, "--config", "c.yaml")...)
	mustContain(t, "settings change", errs, "2 scanned, 0 from cache")
}

func TestOrgLocalWithSink(t *testing.T) {
	work := sandbox(t)
	clones := localClones(t, work, "tf-net")
	out, _ := mustRun(t, "org", "--local-dir", clones, "--org", "acme", "--out", "facts", "--cache-dir", filepath.Join(work, "cd"), "--sink", "markdown")
	mustContain(t, "markdown", out, "### tfmetrics: acme/tf-net")
	if _, _, err := run(t, "org", "--local-dir", clones, "--org", "acme", "--out", "facts", "--cache-dir", filepath.Join(work, "cd"), "--sink", "nope"); err == nil {
		t.Error("unknown sink should fail")
	}
}

func TestOrgConfigOrgsAndFilters(t *testing.T) {
	work := sandbox(t)
	clones := localClones(t, work, "tf-net", "tf-app", "skip-me")
	writeFile(t, filepath.Join(work, "org.yaml"), `
source:
  github:
    orgs:
      - name: acme
        include:
          names: ["*"]
        exclude:
          names: ["skip-*"]
`)
	out, _ := mustRun(t, "org", "--config", "org.yaml", "--local-dir", clones, "--dry-run")
	mustContain(t, "config org", out, "acme/tf-app", "acme/tf-net", "2 repos matched")

	// --org naming a configured org keeps its exclude filter.
	out, _ = mustRun(t, "org", "--config", "org.yaml", "--org", "acme", "--local-dir", clones, "--dry-run")
	mustContain(t, "named configured org", out, "2 repos matched")

	// Filter flags replace the include filters.
	out, _ = mustRun(t, "org", "--config", "org.yaml", "--local-dir", clones, "--dry-run", "--name", "tf-net", "--topic", "x", "--property", "team=a,b", "--property", "team=c")
	mustContain(t, "flag filters", out, "0 repos matched")
}

func TestOrgErrors(t *testing.T) {
	work := sandbox(t)
	clones := localClones(t, work, "tf-net")
	writeFile(t, filepath.Join(work, "two.yaml"), "source:\n  github:\n    orgs:\n      - name: a\n      - name: b\n")
	filePath := filepath.Join(work, "afile")
	writeFile(t, filePath, "x")
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"no orgs", []string{"org", "--dry-run"}, "no orgs: set source.github.orgs in the config or pass --org"},
		{"local-dir with two orgs flags", []string{"org", "--local-dir", clones, "--org", "a", "--org", "b", "--dry-run"}, "--local-dir takes exactly one --org"},
		{"local-dir with two configured orgs", []string{"org", "--local-dir", clones, "--config", "two.yaml", "--dry-run"}, "--local-dir takes exactly one --org"},
		{"bad property", []string{"org", "--org", "a", "--local-dir", clones, "--property", "teamplatform", "--dry-run"}, "property filter"},
		{"empty property value", []string{"org", "--org", "a", "--local-dir", clones, "--property", "team=", "--dry-run"}, "property filter"},
		{"missing local dir", []string{"org", "--org", "a", "--local-dir", filepath.Join(work, "nope"), "--dry-run"}, "no such file"},
		{"bad name regex", []string{"org", "--org", "a", "--local-dir", clones, "--name", "re:(", "--dry-run"}, "error"},
		{"positional arg", []string{"org", "extra"}, "unknown command"},
		{"every repo failed", []string{"org", "--org", "a", "--local-dir", clones, "--out", "facts", "--cache-dir", filepath.Join(filePath, "sub")}, "every repo failed to scan"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, errs, err := run(t, tt.args...)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want containing %q (stderr %q)", err, tt.want, errs)
			}
		})
	}
}

func TestOrgStrict(t *testing.T) {
	work := sandbox(t)
	clones := localClones(t, work, "good", "bad")
	// Make caching fail for one repo only: its cache entry directory is a file.
	cacheDir := filepath.Join(work, "cd")
	writeFile(t, filepath.Join(cacheDir, "acme__bad"), "not a directory")
	args := []string{"org", "--org", "acme", "--local-dir", clones, "--out", "facts", "--cache-dir", cacheDir}

	_, errs := mustRun(t, args...)
	mustContain(t, "partial failure", errs, "error   acme/bad", "scanned acme/good", "1 scanned, 0 from cache, 1 failed")

	_, _, err := run(t, append(args, "--strict")...)
	if err == nil || !strings.Contains(err.Error(), "1 repos failed to scan") {
		t.Errorf("--strict err = %v", err)
	}
}

// fakeGitHub serves the two endpoints org discovery needs.
func fakeGitHub(t *testing.T, repos string, propsStatus int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/orgs/acme/repos", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(repos))
	})
	mux.HandleFunc("/api/v3/orgs/acme/properties/values", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(propsStatus)
		if propsStatus == http.StatusOK {
			_, _ = w.Write([]byte(`[{"repository_name":"tf-net","properties":[{"property_name":"team","value":"platform"}]}]`))
		} else {
			_, _ = w.Write([]byte(`{"message":"nope"}`))
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestOrgGitHubDryRunFake(t *testing.T) {
	work := sandbox(t)
	srv := fakeGitHub(t, `[
	  {"name":"tf-net","full_name":"acme/tf-net","clone_url":"https://example.invalid/acme/tf-net.git","default_branch":"main","topics":["terraform"]},
	  {"name":"old","full_name":"acme/old","clone_url":"https://example.invalid/acme/old.git","archived":true},
	  {"name":"web","full_name":"acme/web","clone_url":"https://example.invalid/acme/web.git","topics":["frontend"]}
	]`, http.StatusOK)
	writeFile(t, filepath.Join(work, "gh.yaml"), fmt.Sprintf("source:\n  github:\n    base_url: %s\n", srv.URL))

	out, _ := mustRun(t, "org", "--config", "gh.yaml", "--org", "acme", "--dry-run")
	mustContain(t, "text", out, "acme/tf-net\ttopics=terraform\tteam=platform", "acme/web", "2 repos matched")
	if strings.Contains(out, "acme/old") {
		t.Errorf("archived repo included:\n%s", out)
	}

	out, _ = mustRun(t, "org", "--config", "gh.yaml", "--org", "acme", "--topic", "terraform", "--dry-run", "--json")
	var repos []map[string]any
	if err := json.Unmarshal([]byte(out), &repos); err != nil || len(repos) != 1 || repos[0]["repo"] != "acme/tf-net" {
		t.Errorf("repos = %v, err %v\n%s", repos, err, out)
	}

	out, _ = mustRun(t, "org", "--config", "gh.yaml", "--org", "acme", "--property", "team=platform", "--dry-run")
	mustContain(t, "property filter", out, "1 repos matched", "acme/tf-net")
}

func TestOrgGitHubPropertiesForbiddenIsTolerated(t *testing.T) {
	work := sandbox(t)
	srv := fakeGitHub(t, `[{"name":"a","full_name":"acme/a","clone_url":"x"}]`, http.StatusForbidden)
	writeFile(t, filepath.Join(work, "gh.yaml"), fmt.Sprintf("source:\n  github:\n    base_url: %s\n", srv.URL))
	out, _ := mustRun(t, "org", "--config", "gh.yaml", "--org", "acme", "--dry-run")
	mustContain(t, "output", out, "1 repos matched")
}

func TestOrgGitHubErrorsFake(t *testing.T) {
	work := sandbox(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"boom"}`, http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	writeFile(t, filepath.Join(work, "gh.yaml"), fmt.Sprintf("source:\n  github:\n    base_url: %s\n    max_retries: -1\n", srv.URL))
	if _, _, err := run(t, "org", "--config", "gh.yaml", "--org", "acme", "--dry-run"); err == nil {
		t.Error("API failure should surface")
	}
}

func TestOrgGitHubFullRunFakeWithBareClone(t *testing.T) {
	work := sandbox(t)
	src := simpleRepo(t, work, "src")
	url := bareRepo(t, src, filepath.Join(work, "remote", "tf-net.git"))
	srv := fakeGitHub(t, fmt.Sprintf(`[{"name":"tf-net","full_name":"acme/tf-net","clone_url":%q,"default_branch":"main","topics":["terraform"]}]`, url), http.StatusNotFound)
	writeFile(t, filepath.Join(work, "gh.yaml"), fmt.Sprintf("source:\n  github:\n    base_url: %s\nscan:\n  cache_dir: %s\n", srv.URL, filepath.Join(work, "cd")))

	_, errs := mustRun(t, "org", "--config", "gh.yaml", "--org", "acme", "--out", "facts", "--concurrency", "2")
	mustContain(t, "first", errs, "scanned acme/tf-net", "1 scanned, 0 from cache")
	r := readFacts(t, filepath.Join("facts", "acme__tf-net.json"))
	if r.Topics[0] != "terraform" || len(r.Commit) != 40 {
		t.Errorf("facts = %+v", r)
	}
	_, errs = mustRun(t, "org", "--config", "gh.yaml", "--org", "acme", "--out", "facts", "--ref", "main")
	mustContain(t, "second", errs, "cached  acme/tf-net")
}

func TestConfigErrors(t *testing.T) {
	work := sandbox(t)
	writeFile(t, filepath.Join(work, "broken.yaml"), "sinks: [unclosed\n")
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"missing explicit config", []string{"scan", fixtureDir, "--config", filepath.Join(work, "nope.yaml")}, "reading config"},
		{"malformed config", []string{"version", "--config", "broken.yaml"}, "reading config"},
		{"missing config for explain", []string{"explain", ".", "--config", "nope.yaml"}, "reading config"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := run(t, tt.args...)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
	// A missing implicit config is fine.
	mustRun(t, "version")
}

func TestEnvOverrides(t *testing.T) {
	work := sandbox(t)
	repo := simpleRepo(t, work, "simple")

	// TFMETRICS_OUT beats the flag default.
	envOut := filepath.Join(work, "env-out")
	t.Setenv("TFMETRICS_OUT", envOut)
	mustRun(t, "scan", repo)
	if _, err := os.Stat(filepath.Join(envOut, "local__simple.json")); err != nil {
		t.Error(err)
	}

	// An explicit flag beats the environment.
	flagOut := filepath.Join(work, "flag-out")
	mustRun(t, "scan", repo, "--out", flagOut)
	if _, err := os.Stat(filepath.Join(flagOut, "local__simple.json")); err != nil {
		t.Error(err)
	}

	// The environment beats the config file.
	writeFile(t, filepath.Join(work, "tfmetrics.yaml"), "scan:\n  ref: nonexistent\n")
	t.Setenv("TFMETRICS_SCAN_REF", "")
	mustRun(t, "scan", repo)

	// Nested keys come from the environment too.
	t.Setenv("TFMETRICS_OWNERS_PROPERTY", "squad")
	mustRun(t, "scan", repo, "--out", filepath.Join(work, "o3"))
}

func TestRootHelpers(t *testing.T) {
	sandbox(t)
	if Root() != rootCmd {
		t.Error("Root() should return the root command")
	}
	if !strings.Contains(joinNames(), "openmetrics") {
		t.Errorf("joinNames = %q", joinNames())
	}
	fs := pflag.NewFlagSet("x", pflag.ContinueOnError)
	fs.StringSlice("a", []string{"x", "y"}, "")
	if got := stringSlice(fs, "a"); len(got) != 2 {
		t.Errorf("stringSlice = %v", got)
	}
	if got := stringSlice(fs, "missing"); len(got) != 0 {
		t.Errorf("stringSlice(missing) = %v", got)
	}
	if err := bindFlags(versionCmd, map[string]string{"nope": "x"}); err == nil || !strings.Contains(err.Error(), `unknown flag "nope"`) {
		t.Errorf("bindFlags err = %v", err)
	}
}

func TestResetFlagsIsolatesRuns(t *testing.T) {
	work := sandbox(t)
	clones := localClones(t, work, "one")
	mustRun(t, "org", "--local-dir", clones, "--org", "acme", "--dry-run")
	// The previous run's --org/--local-dir/--dry-run must not leak.
	_, _, err := run(t, "org", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "no orgs") {
		t.Errorf("flags leaked between runs: %v", err)
	}
}

func TestConfigFileWithoutYAMLExtension(t *testing.T) {
	work := sandbox(t)
	factsDir := scanFixture(t, work)
	prom := filepath.Join(work, "conf-out", "metrics.prom")
	for _, name := range []string{"tfmetrics.conf", "tfmetrics", "tfmetrics.ini", "tfmetrics.YML"} {
		cfg := filepath.Join(work, name)
		writeFile(t, cfg, fmt.Sprintf("sinks:\n  - openmetrics:\n      path: %s\n", prom))
		_ = os.Remove(prom)
		mustRun(t, "aggregate", factsDir, "--offline", "--config", cfg)
		if _, err := os.Stat(prom); err != nil {
			t.Errorf("--config %s: sink from the config did not run: %v", name, err)
		}
	}
	// A known extension still picks its own format.
	cfg := filepath.Join(work, "tfmetrics.json")
	writeFile(t, cfg, fmt.Sprintf(`{"sinks": [{"openmetrics": {"path": %q}}]}`, prom))
	_ = os.Remove(prom)
	mustRun(t, "aggregate", factsDir, "--offline", "--config", cfg)
	if _, err := os.Stat(prom); err != nil {
		t.Errorf("--config tfmetrics.json: %v", err)
	}
}

// TestBinaryInWorkingDirIsNotConfig is the v0.1.0 bug: run from the directory
// it was unpacked into, the tfmetrics binary itself was read as the config.
func TestBinaryInWorkingDirIsNotConfig(t *testing.T) {
	work := sandbox(t)
	writeFile(t, filepath.Join(work, "tfmetrics"), "\x7fELF\x02\x01\x01\x00binary")
	out, _ := mustRun(t, "version")
	mustContain(t, "version", out, "tfmetrics ")

	// A tfmetrics.yml next to it is still found.
	factsDir := scanFixture(t, work)
	prom := filepath.Join(work, "yml-out", "metrics.prom")
	writeFile(t, filepath.Join(work, "tfmetrics.yml"), fmt.Sprintf("sinks:\n  - openmetrics:\n      path: %s\n", prom))
	mustRun(t, "aggregate", factsDir, "--offline")
	if _, err := os.Stat(prom); err != nil {
		t.Errorf("tfmetrics.yml in the working directory was not used: %v", err)
	}
}

func TestScanWriteErrorFails(t *testing.T) {
	work := sandbox(t)
	a := simpleRepo(t, work, "one")
	blocker := filepath.Join(work, "not-a-dir")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, "scan", a, "--out", blocker); err == nil {
		t.Error("want an error when the facts cannot be written")
	}
}

func TestScanConcurrencyFlag(t *testing.T) {
	work := sandbox(t)
	var dirs []string
	for _, n := range []string{"one", "two", "three"} {
		dirs = append(dirs, simpleRepo(t, work, n))
	}
	_, errs := mustRun(t, append(append([]string{"scan"}, dirs...), "--out", "facts", "--concurrency", "1")...)
	mustContain(t, "stderr", errs, "local/one", "local/two", "local/three")
	if got := v.GetInt("scan" + app.KeyDelimiter + "concurrency"); got != 1 {
		t.Errorf("scan.concurrency = %d, want the flag's 1", got)
	}
}
