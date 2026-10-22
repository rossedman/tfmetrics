package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"

	"github.com/rossedman/tfmetrics/internal/config"
	"github.com/rossedman/tfmetrics/internal/discover"
	"github.com/rossedman/tfmetrics/internal/facts"
	"github.com/rossedman/tfmetrics/internal/metrics"
	"github.com/rossedman/tfmetrics/internal/pipeline"
	"github.com/rossedman/tfmetrics/internal/report"
	"github.com/rossedman/tfmetrics/internal/sink"
	"github.com/rossedman/tfmetrics/internal/source"
)

const fullYAML = `
source:
  github:
    token_env: MY_TOKEN
    base_url: https://ghe.example.com/api/v3/
    clone_url: https://ghe.example.com
    defaults:
      archived: true
    orgs:
      - name: acme
        include:
          names: ["tf-*"]
          topics: [terraform]
          properties:
            team: [platform, networking]
        exclude:
          names: ["tf-sandbox"]
      - name: acme-labs
scan:
  concurrency: 4
  ignore_dirs: [.git, vendor]
  cache_dir: /var/cache/tfm
  root_overrides: [live/*]
  ref: main
modules:
  internal_registry: App.Terraform.IO
  registry_token_env: REG_TOKEN
  registries:
    - host: gitlab.com
      token_env: GITLAB_TOKEN
    - host: tf.acme.io
  aliases:
    github.com/acme/old: app.terraform.io/acme/new/aws
labels:
  properties: [team, tier]
  drop: [resource_type]
owners:
  property: squad
hygiene:
  weights:
    pinned: 50
    parse_clean: 5
sinks:
  - openmetrics:
      path: out/m.prom
      job: custom
  - json:
      path: out/json
`

func viperFrom(t *testing.T, yaml string) *viper.Viper {
	t.Helper()
	v := NewViper()
	v.SetConfigType("yaml")
	if err := v.ReadConfig(strings.NewReader(yaml)); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestLoadConfigFull(t *testing.T) {
	t.Parallel()
	c, err := LoadConfig(viperFrom(t, fullYAML))
	if err != nil {
		t.Fatal(err)
	}
	gh := c.Source.GitHub
	if gh.TokenEnv != "MY_TOKEN" || gh.BaseURL != "https://ghe.example.com/api/v3/" || gh.CloneURL != "https://ghe.example.com" || !gh.Defaults.Archived || gh.Defaults.Forks {
		t.Errorf("github = %+v", gh)
	}
	if len(gh.Orgs) != 2 {
		t.Fatalf("orgs = %+v", gh.Orgs)
	}
	o := gh.Orgs[0]
	if o.Name != "acme" || o.Include.Names[0] != "tf-*" || o.Include.Topics[0] != "terraform" ||
		len(o.Include.Properties["team"]) != 2 || o.Exclude.Names[0] != "tf-sandbox" {
		t.Errorf("org[0] = %+v", o)
	}
	if !gh.Orgs[1].Include.Empty() || !gh.Orgs[1].Exclude.Empty() {
		t.Errorf("org[1] filters should be empty: %+v", gh.Orgs[1])
	}
	if c.Scan.Concurrency != 4 || c.Scan.CacheDir != "/var/cache/tfm" || c.Scan.Ref != "main" ||
		len(c.Scan.IgnoreDirs) != 2 || c.Scan.RootOverrides[0] != "live/*" {
		t.Errorf("scan = %+v", c.Scan)
	}
	if c.Modules.InternalRegistry != "App.Terraform.IO" || c.Modules.RegistryTokenEnv != "REG_TOKEN" || len(c.Modules.Aliases) != 1 ||
		len(c.Modules.Registries) != 2 || c.Modules.Registries[0] != (config.Registry{Host: "gitlab.com", TokenEnv: "GITLAB_TOKEN"}) {
		t.Errorf("modules = %+v", c.Modules)
	}
	if len(c.Labels.Properties) != 2 || c.Labels.Drop[0] != "resource_type" {
		t.Errorf("labels = %+v", c.Labels)
	}
	if c.Owners.Property != "squad" {
		t.Errorf("owners = %+v", c.Owners)
	}
	if c.Hygiene.Weights["pinned"] != 50 || len(c.Hygiene.Weights) != 2 {
		t.Errorf("weights = %+v", c.Hygiene.Weights)
	}
	if len(c.Sinks) != 2 || c.Sinks[0]["openmetrics"]["job"] != "custom" {
		t.Errorf("sinks = %+v", c.Sinks)
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	t.Parallel()
	c, err := LoadConfig(NewViper())
	if err != nil {
		t.Fatal(err)
	}
	if c.Source.GitHub.TokenEnv != "GITHUB_TOKEN" || c.Scan.Concurrency != 16 || c.Owners.Property != "owner" {
		t.Errorf("defaults not applied: %+v", c)
	}
	if len(c.Hygiene.Weights) != len(config.DefaultWeights) {
		t.Errorf("weights = %v", c.Hygiene.Weights)
	}
}

func TestLoadConfigErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, yaml, want string
	}{
		{"unknown weight", "hygiene:\n  weights:\n    nope: 1\n", `unknown key "nope"`},
		{"org without name", "source:\n  github:\n    orgs:\n      - include:\n          names: [x]\n", "orgs[0]: name is required"},
		{"decode error", "scan:\n  concurrency: [1, 2]\n", "config:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := LoadConfig(viperFrom(t, tt.yaml))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestNormalizerAndScanOptions(t *testing.T) {
	t.Parallel()
	c, err := LoadConfig(viperFrom(t, fullYAML))
	if err != nil {
		t.Fatal(err)
	}
	n := Normalizer(c)
	if got := n.Classify("app.terraform.io/acme/vpc/aws"); got.Kind != source.KindRegistry && got.Kind != source.KindPrivateRegistry {
		t.Errorf("classify = %+v", got)
	}
	if got := n.Classify("github.com/acme/old"); got.Address != "app.terraform.io/acme/new/aws" {
		t.Errorf("alias not applied: %+v", got)
	}
	so := ScanOptions(c)
	if so.OwnerProperty != "squad" || so.Normalizer == nil || len(so.IgnoreDirs) != 2 || so.RootOverrides[0] != "live/*" {
		t.Errorf("scan options = %+v", so)
	}
}

func TestSettings(t *testing.T) {
	t.Parallel()
	base := config.Default()
	a := Settings(base, "v1")
	if b := Settings(base, "v1"); a != b {
		t.Errorf("Settings not stable:\n%s\n%s", a, b)
	}
	// Map ordering must not matter: aliases are marshalled sorted.
	withAliases := config.Default()
	withAliases.Modules.Aliases = map[string]string{"a": "1", "b": "2", "c": "3", "d": "4"}
	first := Settings(withAliases, "v1")
	for range 20 {
		if got := Settings(withAliases, "v1"); got != first {
			t.Fatal("Settings unstable across map iteration orders")
		}
	}
	mod := []struct {
		name string
		edit func(*config.Config) string
	}{
		{"version", func(*config.Config) string { return "v2" }},
		{"ignore dirs", func(c *config.Config) string { c.Scan.IgnoreDirs = []string{"x"}; return "v1" }},
		{"root overrides", func(c *config.Config) string { c.Scan.RootOverrides = []string{"x"}; return "v1" }},
		{"registry", func(c *config.Config) string { c.Modules.InternalRegistry = "r.example"; return "v1" }},
		{"registries", func(c *config.Config) string {
			c.Modules.Registries = []config.Registry{{Host: "gitlab.com"}}
			return "v1"
		}},
		{"aliases", func(c *config.Config) string { c.Modules.Aliases = map[string]string{"a": "b"}; return "v1" }},
		{"owner property", func(c *config.Config) string { c.Owners.Property = "squad"; return "v1" }},
	}
	for _, m := range mod {
		c := config.Default()
		ver := m.edit(&c)
		if Settings(c, ver) == a {
			t.Errorf("changing %s does not change Settings", m.name)
		}
	}
	// Things that do not affect scan output must not change the key.
	c := config.Default()
	c.Scan.Concurrency = 1
	c.Hygiene.Weights = map[string]float64{"pinned": 1}
	c.Labels.Drop = []string{"x"}
	if Settings(c, "v1") != a {
		t.Error("concurrency/weights/labels should not affect Settings")
	}
}

func TestGitHubTokenAndResolver(t *testing.T) {
	c := config.Default()
	c.Source.GitHub.TokenEnv = "TFM_TEST_GH"
	c.Modules.InternalRegistry = "App.Terraform.IO"
	c.Modules.RegistryTokenEnv = "TFM_TEST_REG"

	t.Setenv("TFM_TEST_GH", "")
	t.Setenv("TFM_TEST_REG", "")
	if got := GitHubToken(c); got != "" {
		t.Errorf("token = %q, want empty", got)
	}
	r := Resolver(c)
	if r.GitToken != "" || r.GitTokenHost != "" {
		t.Errorf("git token set without env: %+v", r)
	}

	t.Setenv("TFM_TEST_GH", "ghp_x")
	t.Setenv("TFM_TEST_REG", "reg_y")
	if got := GitHubToken(c); got != "ghp_x" {
		t.Errorf("token = %q", got)
	}
	r = Resolver(c)
	if r.GitToken != "ghp_x" || r.GitTokenHost != "github.com" {
		t.Errorf("git token = %q @ %q", r.GitToken, r.GitTokenHost)
	}
	if r.Tokens["app.terraform.io"] != "reg_y" || len(r.Tokens) != 1 {
		t.Errorf("registry tokens = %v (host should be lower-cased)", r.Tokens)
	}

	// Several registries, each with its own token; one without a token_env
	// and one whose variable is empty get no entry.
	t.Setenv("TFM_TEST_GITLAB", "gl_z")
	t.Setenv("TFM_TEST_EMPTY", "")
	c.Modules.Registries = []config.Registry{
		{Host: "GitLab.com", TokenEnv: "TFM_TEST_GITLAB"},
		{Host: "tf.acme.io"},
		{Host: "empty.example", TokenEnv: "TFM_TEST_EMPTY"},
	}
	want := map[string]string{"app.terraform.io": "reg_y", "gitlab.com": "gl_z"}
	if got := Resolver(c).Tokens; len(got) != len(want) || got["app.terraform.io"] != "reg_y" || got["gitlab.com"] != "gl_z" {
		t.Errorf("registry tokens = %v, want %v", got, want)
	}
	if n := Normalizer(c); !n.Registry("gitlab.com") || !n.Registry("tf.acme.io") || !n.Registry("app.terraform.io") {
		t.Error("normalizer should know every configured registry")
	}
	c.Modules.Registries = nil

	// No internal registry: no registry token entry.
	c.Modules.InternalRegistry = ""
	if got := Resolver(c).Tokens; len(got) != 0 {
		t.Errorf("tokens = %v, want none", got)
	}
	// Registry without a token env: also none.
	c.Modules.InternalRegistry, c.Modules.RegistryTokenEnv = "r.example", ""
	if got := Resolver(c).Tokens; len(got) != 0 {
		t.Errorf("tokens = %v, want none", got)
	}
}

type stubLister struct {
	err error
}

func (s stubLister) Versions(context.Context, string, string) ([]string, error) {
	return []string{"1.0.0", "1.1.0", "2.0.0"}, s.err
}

func aggInput() []facts.ScanResult {
	return []facts.ScanResult{{
		Repo: "acme/x", Org: "acme",
		Properties: map[string]string{"team": "core"},
		Modules: []facts.ModuleFacts{{
			Kind:        facts.KindRoot,
			ModuleCalls: []facts.ModuleCall{{SourceKind: source.KindRegistry, Address: "registry.terraform.io/a/b/c", Version: "1.0.0", Pinned: facts.PinnedExact}},
			Resources:   []facts.BlockRef{{Type: "aws_x", Provider: "aws"}},
		}},
	}}
}

func TestAggregate(t *testing.T) {
	t.Parallel()
	c := config.Default()
	c.Labels.Properties = []string{"team"}

	var warn bytes.Buffer
	var lookups [][2]int
	hooks := Hooks{
		Warn:   func(msg string) { fmt.Fprintln(&warn, "warning:", msg) },
		Lookup: func(done, total int) { lookups = append(lookups, [2]int{done, total}) },
	}
	m := Aggregate(context.Background(), c, aggInput(), nil, hooks)
	if m.Repos != 1 {
		t.Errorf("repos = %d", m.Repos)
	}
	for _, s := range m.Series {
		if s.Name == metrics.ModuleVersionsBehind {
			t.Error("versions_behind with nil lister")
		}
		if s.Name == metrics.ModulesTotal && s.Labels["team"] != "core" {
			t.Errorf("team label missing: %v", s.Labels)
		}
	}
	if warn.Len() != 0 {
		t.Errorf("warnings with nil lister: %q", warn.String())
	}

	m = Aggregate(context.Background(), c, aggInput(), stubLister{}, hooks)
	if want := [][2]int{{0, 1}, {1, 1}}; !reflect.DeepEqual(lookups, want) {
		t.Errorf("lookup progress = %v, want %v", lookups, want)
	}
	found := false
	for _, s := range m.Series {
		if s.Name == metrics.ModuleVersionsBehind && s.Value == 2 {
			found = true
		}
	}
	if !found {
		t.Error("versions_behind = 2 expected with lister")
	}

	_ = Aggregate(context.Background(), c, aggInput(), stubLister{err: errors.New("registry down")}, hooks)
	_ = Aggregate(context.Background(), c, aggInput(), stubLister{err: errors.New("unheard")}, Hooks{}) // nil hooks are skipped
	if got := warn.String(); !strings.Contains(got, "warning: version lookup for registry.terraform.io/a/b/c: registry down") {
		t.Errorf("warn = %q", got)
	}

	// Rollup via config.
	c.Labels.Drop = []string{"resource_type", "provider"}
	m = Aggregate(context.Background(), c, aggInput(), nil, hooks)
	for _, s := range m.Series {
		if _, ok := s.Labels["resource_type"]; ok {
			t.Errorf("resource_type not dropped: %v", s.Labels)
		}
	}
}

func TestSinkSpecs(t *testing.T) {
	t.Parallel()
	c, err := LoadConfig(viperFrom(t, fullYAML))
	if err != nil {
		t.Fatal(err)
	}
	t.Run("config when no names", func(t *testing.T) {
		t.Parallel()
		got, err := SinkSpecs(c, nil)
		if err != nil || len(got) != 2 || got[0].Name != "openmetrics" || got[0].Options["job"] != "custom" || got[1].Name != "json" {
			t.Errorf("got %+v, %v", got, err)
		}
	})
	t.Run("names inherit config options", func(t *testing.T) {
		t.Parallel()
		got, err := SinkSpecs(c, []string{"json", "csv", "openmetrics"})
		if err != nil || len(got) != 3 {
			t.Fatalf("got %+v, %v", got, err)
		}
		if got[0].Options["path"] != "out/json" {
			t.Errorf("json options = %v", got[0].Options)
		}
		if got[1].Name != "csv" || got[1].Options != nil {
			t.Errorf("csv = %+v, want no options", got[1])
		}
		if got[2].Options["path"] != "out/m.prom" {
			t.Errorf("openmetrics options = %v", got[2].Options)
		}
	})
	t.Run("none anywhere", func(t *testing.T) {
		t.Parallel()
		got, err := SinkSpecs(config.Default(), nil)
		if err != nil || len(got) != 0 {
			t.Errorf("got %+v, %v", got, err)
		}
	})
	t.Run("bad shape", func(t *testing.T) {
		t.Parallel()
		bad := config.Default()
		bad.Sinks = []map[string]map[string]any{{"json": nil, "csv": nil}}
		for _, names := range [][]string{nil, {"json"}} {
			if _, err := SinkSpecs(bad, names); err == nil || !strings.Contains(err.Error(), "sinks[0]") {
				t.Errorf("names=%v err = %v", names, err)
			}
		}
	})
}

func TestEmit(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	res := aggInput()
	m := Aggregate(context.Background(), config.Default(), res, nil, Hooks{})

	t.Run("writes every sink", func(t *testing.T) {
		t.Parallel()
		out := filepath.Join(dir, "ok")
		specs := []sink.Spec{{Name: "json"}, {Name: "openmetrics"}, {Name: "csv"}}
		if err := Emit(context.Background(), specs, sink.Env{OutDir: out}, res, m); err != nil {
			t.Fatal(err)
		}
		for _, f := range []string{"metrics.json", "metrics.prom"} {
			if _, err := os.Stat(filepath.Join(out, f)); err != nil {
				t.Error(err)
			}
		}
	})
	t.Run("unknown sink", func(t *testing.T) {
		t.Parallel()
		err := Emit(context.Background(), []sink.Spec{{Name: "nope"}}, sink.Env{OutDir: dir}, res, m)
		if err == nil || !strings.Contains(err.Error(), `unknown sink "nope"`) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("emit error wrapped with sink name", func(t *testing.T) {
		t.Parallel()
		specs := []sink.Spec{{Name: "json", Options: map[string]any{"path": "s3://bucket/x"}}}
		err := Emit(context.Background(), specs, sink.Env{OutDir: dir}, res, m)
		if err == nil || !strings.HasPrefix(err.Error(), "sink json: ") || !strings.Contains(err.Error(), "only local paths") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("stops at first error", func(t *testing.T) {
		t.Parallel()
		out := filepath.Join(dir, "stop")
		specs := []sink.Spec{{Name: "nope"}, {Name: "json"}}
		if err := Emit(context.Background(), specs, sink.Env{OutDir: out}, res, m); err == nil {
			t.Fatal("want error")
		}
		if _, err := os.Stat(out); err == nil {
			t.Error("second sink ran after the first failed")
		}
	})
	t.Run("no specs", func(t *testing.T) {
		t.Parallel()
		if err := Emit(context.Background(), nil, sink.Env{}, res, m); err != nil {
			t.Error(err)
		}
	})
}

func TestRemoteURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, arg, base, want string
		wantErr               bool
	}{
		{"https url", "https://github.com/acme/x.git", "", "https://github.com/acme/x.git", false},
		{"file url", "file:///tmp/x.git", "", "file:///tmp/x.git", false},
		{"ssh url", "ssh://git@host/acme/x.git", "", "ssh://git@host/acme/x.git", false},
		{"scp style", "git@github.com:acme/x.git", "", "git@github.com:acme/x.git", false},
		{"owner/name default base", "acme/x", "", "https://github.com/acme/x.git", false},
		{"owner/name custom base", "acme/x", "https://ghe.example.com", "https://ghe.example.com/acme/x.git", false},
		{"base trailing slash", "acme/x", "https://ghe.example.com/", "https://ghe.example.com/acme/x.git", false},
		{"dot prefix", "./x", "", "", true},
		{"dotdot", "../x", "", "", true},
		{"single word", "x", "", "", true},
		{"three parts", "a/b/c", "", "", true},
		{"empty owner", "/x", "", "", true},
		{"empty name", "acme/", "", "", true},
		{"empty", "", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := RemoteURL(tt.arg, tt.base)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("RemoteURL(%q, %q) = %q, want %q", tt.arg, tt.base, got, tt.want)
			}
			if err != nil && !strings.Contains(err.Error(), "not a directory, URL or owner/name") {
				t.Errorf("err = %v", err)
			}
		})
	}
}

func TestNewViperEnv(t *testing.T) {
	t.Setenv("TFMETRICS_SCAN_CACHE_DIR", "/from/env")
	t.Setenv("TFMETRICS_SCAN_CONCURRENCY", "3")
	v := NewViper()
	v.SetConfigType("yaml")
	if err := v.ReadConfig(strings.NewReader("scan:\n  cache_dir: /from/file\n  concurrency: 9\n")); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(v)
	if err != nil {
		t.Fatal(err)
	}
	if c.Scan.CacheDir != "/from/env" || c.Scan.Concurrency != 3 {
		t.Errorf("env should beat file: %+v", c.Scan)
	}
}

func TestDottedAliasKeys(t *testing.T) {
	t.Parallel()
	yaml := "modules:\n  aliases:\n" +
		"    github.com/acme/old: app.terraform.io/acme/new/aws\n" +
		"    \"git::https://github.com/acme/tf-vpc.git\": tfe.acme.internal/acme/vpc/aws\n"
	c, err := LoadConfig(viperFrom(t, yaml))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"github.com/acme/old":                     "app.terraform.io/acme/new/aws",
		"git::https://github.com/acme/tf-vpc.git": "tfe.acme.internal/acme/vpc/aws",
	}
	for k, v := range want {
		if got := c.Modules.Aliases[k]; got != v {
			t.Errorf("aliases[%q] = %q, want %q (all: %v)", k, got, v, c.Modules.Aliases)
		}
	}
}

func TestRunStats(t *testing.T) {
	t.Parallel()
	sum := pipeline.Summary{Wall: 2 * time.Second, RemoteHeads: 3, HeadTime: time.Second, Clones: 5, CloneTime: 4 * time.Second, ScanTime: 300 * time.Millisecond}
	got := RunStats(sum, nil)
	for _, want := range []string{"2s wall", "ls-remote 1s (3)", "clone 4s (5)", "parse 300ms"} {
		if !strings.Contains(got, want) {
			t.Errorf("RunStats = %q, missing %q", got, want)
		}
	}
	if strings.Contains(got, "github api") {
		t.Errorf("no API line without discovery: %q", got)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "4990")
		w.Header().Set("X-RateLimit-Reset", "1800000000")
		w.Header().Set("X-RateLimit-Resource", "core")
		_, _ = w.Write([]byte("[]"))
	}))
	t.Cleanup(srv.Close)
	gh, err := discover.NewGitHub("tok", srv.URL, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gh.Org(context.Background(), config.Org{Name: "acme"}, config.RepoDefault{}); err != nil {
		t.Fatal(err)
	}
	got = RunStats(sum, gh)
	for _, want := range []string{"github api: 2 requests, 0 retries", "core 4990/5000 left"} {
		if !strings.Contains(got, want) {
			t.Errorf("RunStats = %q, missing %q", got, want)
		}
	}
}

func TestFindConfig(t *testing.T) {
	t.Parallel()
	a, b, empty := t.TempDir(), t.TempDir(), t.TempDir()
	// A binary named tfmetrics, and a directory named like a config, are never configs.
	if err := os.WriteFile(filepath.Join(empty, "tfmetrics"), []byte("\x7fELF\x00"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(empty, "tfmetrics.yaml"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{filepath.Join(a, "tfmetrics.yml"), filepath.Join(b, "tfmetrics.yaml"), filepath.Join(b, "tfmetrics.yml")} {
		if err := os.WriteFile(f, []byte("scan: {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name string
		dirs []string
		want string
	}{
		{"none", []string{empty}, ""},
		{"yml", []string{empty, a}, filepath.Join(a, "tfmetrics.yml")},
		{"yaml before yml in one dir", []string{b}, filepath.Join(b, "tfmetrics.yaml")},
		{"earlier dir wins", []string{a, b}, filepath.Join(a, "tfmetrics.yml")},
		{"no dirs", nil, ""},
	}
	for _, tt := range tests {
		if got := FindConfig(tt.dirs); got != tt.want {
			t.Errorf("%s: FindConfig = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestTuneGC(t *testing.T) {
	prev := debug.SetGCPercent(100)
	defer debug.SetGCPercent(prev)
	TuneGC(func(string) string { return "" })
	if got := debug.SetGCPercent(100); got != DefaultGCPercent {
		t.Errorf("GC percent = %d, want %d", got, DefaultGCPercent)
	}
	TuneGC(func(k string) string {
		if k == "GOGC" {
			return "50"
		}
		return ""
	})
	if got := debug.SetGCPercent(100); got != 100 {
		t.Errorf("GOGC set: GC percent = %d, want it left alone", got)
	}
}

func TestGaps(t *testing.T) {
	t.Parallel()
	in := aggInput()
	in[0].Modules[0].Path = "live"
	in[0].Modules[0].ModuleCalls[0].Name = "c"
	in[0].Modules[0].ModuleCalls = append(in[0].Modules[0].ModuleCalls,
		facts.ModuleCall{Name: "again", SourceKind: source.KindRegistry, Address: "registry.terraform.io/a/b/c", Version: "1.1.0", Pinned: facts.PinnedExact},
		facts.ModuleCall{Name: "unpinned", SourceKind: source.KindGit, Address: "github.com/a/b", Pinned: facts.PinnedNone})
	// Providers and Terraform itself are not looked up.
	in[0].Modules[0].Providers = []facts.ProviderReq{{Name: "aws", Source: "hashicorp/aws", Constraint: "~> 5.0"}}
	in[0].Modules[0].RequiredVersion = ">= 1.5"
	in = append(in, facts.ScanResult{Repo: "acme/empty"})

	var lookups [][2]int
	gaps := Gaps(context.Background(), in, stubLister{}, Hooks{Lookup: func(done, total int) { lookups = append(lookups, [2]int{done, total}) }})
	if len(gaps) != 2 || len(gaps[0]) != 2 || len(gaps[1]) != 0 {
		t.Fatalf("gaps = %v", gaps)
	}
	if g := gaps[0][report.GapKey("live", "c")]; g.Major != 1 || g.Minor != 1 || g.Latest.String() != "2.0.0" {
		t.Errorf("c: %+v", g)
	}
	if g := gaps[0][report.GapKey("live", "again")]; g.Major != 1 || g.Minor != 0 {
		t.Errorf("again: %+v", g)
	}
	if want := [][2]int{{0, 1}, {1, 1}}; !reflect.DeepEqual(lookups, want) {
		t.Errorf("one lookup per address: %v", lookups)
	}

	// A failed lookup warns once per address and leaves its calls out.
	var warns []string
	gaps = Gaps(context.Background(), in, stubLister{err: errors.New("boom")}, Hooks{Warn: func(msg string) { warns = append(warns, msg) }})
	if len(gaps[0]) != 0 || len(warns) != 1 || warns[0] != "version lookup for registry.terraform.io/a/b/c: boom" {
		t.Errorf("gaps = %v, warnings = %q", gaps, warns)
	}
}
