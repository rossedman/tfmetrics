package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/rossedman/tfmetrics/internal/classify"
)

func TestDefault(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	c := Default()
	if c.Source.GitHub.TokenEnv != "GITHUB_TOKEN" {
		t.Errorf("TokenEnv = %q", c.Source.GitHub.TokenEnv)
	}
	if c.Scan.Concurrency != 16 {
		t.Errorf("Concurrency = %d", c.Scan.Concurrency)
	}
	if !reflect.DeepEqual(c.Scan.IgnoreDirs, classify.DefaultIgnoreDirs) {
		t.Errorf("IgnoreDirs = %v", c.Scan.IgnoreDirs)
	}
	if filepath.Base(c.Scan.CacheDir) != "tfmetrics" {
		t.Errorf("CacheDir = %q", c.Scan.CacheDir)
	}
	if c.Owners.Property != "owner" {
		t.Errorf("Owners.Property = %q", c.Owners.Property)
	}
	if !reflect.DeepEqual(c.Hygiene.Weights, DefaultWeights) {
		t.Errorf("Weights = %v", c.Hygiene.Weights)
	}
	var sum float64
	for _, w := range c.Hygiene.Weights {
		sum += w
	}
	if sum != 100 {
		t.Errorf("default weights sum to %v, want 100", sum)
	}
	if err := c.Validate(); err != nil {
		t.Errorf("defaults should validate: %v", err)
	}
}

func TestDefaultsDoNotAliasGlobals(t *testing.T) {
	t.Parallel()
	c := Default()
	c.Scan.IgnoreDirs[0] = "changed"
	c.Hygiene.Weights["pinned"] = 1
	if classify.DefaultIgnoreDirs[0] == "changed" {
		t.Error("IgnoreDirs aliases classify.DefaultIgnoreDirs")
	}
	if DefaultWeights["pinned"] == 1 {
		t.Error("Weights alias DefaultWeights")
	}
}

func TestApplyDefaultsKeepsSetValues(t *testing.T) {
	t.Parallel()
	c := Config{
		Source:  Source{GitHub: GitHub{TokenEnv: "MY_TOKEN"}},
		Scan:    Scan{Concurrency: 4, IgnoreDirs: []string{}, CacheDir: "/tmp/cache"},
		Owners:  Owners{Property: "squad"},
		Hygiene: Hygiene{Weights: map[string]float64{"pinned": 50}},
	}
	c.ApplyDefaults()
	if c.Source.GitHub.TokenEnv != "MY_TOKEN" || c.Scan.Concurrency != 4 || c.Scan.CacheDir != "/tmp/cache" || c.Owners.Property != "squad" {
		t.Errorf("set values overwritten: %+v", c)
	}
	if c.Scan.IgnoreDirs == nil || len(c.Scan.IgnoreDirs) != 0 {
		t.Errorf("explicit empty IgnoreDirs overwritten: %v", c.Scan.IgnoreDirs)
	}
	if len(c.Hygiene.Weights) != 1 || c.Hygiene.Weights["pinned"] != 50 {
		t.Errorf("weights overwritten: %v", c.Hygiene.Weights)
	}
	neg := Config{Scan: Scan{Concurrency: -3}}
	neg.ApplyDefaults()
	if neg.Scan.Concurrency != 16 {
		t.Errorf("negative concurrency -> %d", neg.Scan.Concurrency)
	}
}

func TestApplyDefaultsExpandsHomeInCacheDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	c := Config{Scan: Scan{CacheDir: "~/.cache/tf"}}
	c.ApplyDefaults()
	if want := filepath.Join(home, ".cache", "tf"); c.Scan.CacheDir != want {
		t.Errorf("CacheDir = %q, want %q", c.Scan.CacheDir, want)
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		c       Config
		wantErr string
	}{
		{"ok", Config{Hygiene: Hygiene{Weights: map[string]float64{"pinned": 10}}, Source: Source{GitHub: GitHub{Orgs: []Org{{Name: "acme"}}}}}, ""},
		{"empty ok", Config{}, ""},
		{"unknown weight", Config{Hygiene: Hygiene{Weights: map[string]float64{"bogus": 1}}}, `unknown key "bogus"`},
		{"blank org name", Config{Source: Source{GitHub: GitHub{Orgs: []Org{{Name: "acme"}, {Name: "  "}}}}}, "orgs[1]: name is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.c.Validate()
			switch {
			case tt.wantErr == "" && err != nil:
				t.Errorf("unexpected error %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Errorf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestFilterEmpty(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		f    Filter
		want bool
	}{
		{"zero", Filter{}, true},
		{"empty non-nil", Filter{Names: []string{}, Properties: map[string][]string{}}, true},
		{"names", Filter{Names: []string{"a"}}, false},
		{"topics", Filter{Topics: []string{"a"}}, false},
		{"properties", Filter{Properties: map[string][]string{"k": {"v"}}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.f.Empty(); got != tt.want {
				t.Errorf("Empty = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExpandHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	tests := []struct {
		in   string
		want string
	}{
		{"~", home},
		{"~/", home},
		{"~/foo/bar", filepath.Join(home, "foo", "bar")},
		{"/abs/path", "/abs/path"},
		{"rel/path", "rel/path"},
		{"~user/x", "~user/x"},
		{"a/~/b", "a/~/b"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := ExpandHome(tt.in); got != tt.want {
			t.Errorf("ExpandHome(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestExpandHomeNoHome(t *testing.T) {
	t.Setenv("HOME", "")
	if _, err := os.UserHomeDir(); err == nil {
		t.Skip("home dir resolvable without HOME on this platform")
	}
	if got := ExpandHome("~/x"); got != "~/x" {
		t.Errorf("got %q, want unchanged", got)
	}
}

func TestApplyDefaultsNoUserCacheDir(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("HOME", "")
	if _, err := os.UserCacheDir(); err == nil {
		t.Skip("cache dir resolvable")
	}
	c := Default()
	if c.Scan.CacheDir != "" {
		t.Errorf("CacheDir = %q, want empty (caching disabled)", c.Scan.CacheDir)
	}
}

func TestAllRegistries(t *testing.T) {
	t.Parallel()
	m := Modules{
		InternalRegistry: " App.Terraform.IO ",
		RegistryTokenEnv: "TFE_TOKEN",
		Registries: []Registry{
			{Host: "GitLab.com", TokenEnv: " GITLAB_TOKEN "},
			{Host: ""},
			{Host: "gitlab.com", TokenEnv: "DUP"},
		},
	}
	want := []Registry{{Host: "gitlab.com", TokenEnv: "GITLAB_TOKEN"}, {Host: "app.terraform.io", TokenEnv: "TFE_TOKEN"}}
	if got := m.AllRegistries(); !reflect.DeepEqual(got, want) {
		t.Errorf("AllRegistries = %+v, want %+v", got, want)
	}
	// An explicit entry for the internal_registry host wins.
	m.Registries = []Registry{{Host: "app.terraform.io", TokenEnv: "OTHER"}}
	want = []Registry{{Host: "app.terraform.io", TokenEnv: "OTHER"}}
	if got := m.AllRegistries(); !reflect.DeepEqual(got, want) {
		t.Errorf("AllRegistries = %+v, want %+v", got, want)
	}
	if got := (Modules{}).AllRegistries(); got != nil {
		t.Errorf("empty AllRegistries = %+v", got)
	}
}

func TestValidateRegistries(t *testing.T) {
	t.Parallel()
	for name, tt := range map[string]struct {
		regs []Registry
		err  string
	}{
		"ok":        {[]Registry{{Host: "gitlab.com"}, {Host: "tf.acme.io:8443", TokenEnv: "X"}}, ""},
		"no host":   {[]Registry{{TokenEnv: "X"}}, "modules.registries[0]: host is required"},
		"scheme":    {[]Registry{{Host: "https://tf.acme.io"}}, "without a scheme or path"},
		"duplicate": {[]Registry{{Host: "gitlab.com"}, {Host: "GitLab.com"}}, "modules.registries[1]: host \"GitLab.com\" is listed twice"},
	} {
		c := Default()
		c.Modules.Registries = tt.regs
		err := c.Validate()
		if tt.err == "" {
			if err != nil {
				t.Errorf("%s: %v", name, err)
			}
		} else if err == nil || !strings.Contains(err.Error(), tt.err) {
			t.Errorf("%s: err = %v, want %q", name, err, tt.err)
		}
	}
}
