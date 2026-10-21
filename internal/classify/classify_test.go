package classify

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func fixtureRoot() string {
	return filepath.Join("..", "..", "testdata", "repos", "acme-infra")
}

func TestIsTerraformFile(t *testing.T) {
	t.Parallel()
	tests := map[string]bool{
		"main.tf": true, "main.tf.json": true, "main.tofu": true, "main.tofu.json": true,
		"main.json": false, "main.tfvars": false, "README.md": false, "tf": false, "": false,
	}
	for name, want := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := IsTerraformFile(name); got != want {
				t.Errorf("IsTerraformFile(%q) = %v, want %v", name, got, want)
			}
		})
	}
}

func TestIgnored(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		rel      string
		patterns []string
		want     bool
	}{
		{"base name", "examples", []string{"examples"}, true},
		{"base name nested", "modules/vpc/examples", []string{"examples"}, true},
		{"base miss", "modules/vpc", []string{"examples"}, false},
		{"glob base", "live/test-eu", []string{"test-*"}, true},
		{"slash pattern matches whole path", "live/dev", []string{"live/dev"}, true},
		{"slash pattern does not match base only", "other/dev", []string{"live/dev"}, false},
		{"slash glob", "live/dev", []string{"live/*"}, true},
		{"slash glob depth", "live/dev/x", []string{"live/*"}, false},
		{"surrounding slashes trimmed", "examples", []string{"/examples/"}, true},
		{"empty pattern skipped", "examples", []string{"", "/"}, false},
		{"bad pattern ignored", "examples", []string{"[x"}, false},
		{"no patterns", "examples", nil, false},
		{"second pattern", "test", []string{"nope", "test"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := Ignored(tt.rel, tt.patterns); got != tt.want {
				t.Errorf("Ignored(%q, %v) = %v, want %v", tt.rel, tt.patterns, got, tt.want)
			}
		})
	}
}

func TestKind(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		rel       string
		s         Signals
		overrides []string
		want      string
	}{
		{"no signals", "modules/vpc", Signals{}, nil, "child"},
		{"backend", "live/prod", Signals{Backend: true}, nil, "root"},
		{"cloud", "live/staging", Signals{Cloud: true}, nil, "root"},
		{"configured provider", "live/x", Signals{ConfiguredProvider: true}, nil, "root"},
		{"override exact", "stacks/a", Signals{}, []string{"stacks/a"}, "root"},
		{"override glob", "stacks/a", Signals{}, []string{"stacks/*"}, "root"},
		{"override trims slashes", "stacks/a", Signals{}, []string{"/stacks/a/"}, "root"},
		{"override miss", "stacks/a/b", Signals{}, []string{"stacks/*"}, "child"},
		{"override bad pattern", "stacks/a", Signals{}, []string{"[x"}, "child"},
		{"signal beats override miss", "x", Signals{Backend: true}, []string{"nope"}, "root"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := Kind(tt.rel, tt.s, tt.overrides); got != tt.want {
				t.Errorf("Kind = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFindModuleDirsFixture(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		ignore []string
		want   []string
	}{
		{"defaults skip examples", DefaultIgnoreDirs, []string{"broken", "live/prod", "live/staging", "modules/legacy", "modules/vpc"}},
		{"no ignores includes examples", nil, []string{"broken", "examples/basic", "live/prod", "live/staging", "modules/legacy", "modules/vpc"}},
		{"ignore by path", []string{"examples", "live/staging"}, []string{"broken", "live/prod", "modules/legacy", "modules/vpc"}},
		{"ignore by glob", []string{"examples", "mod*"}, []string{"broken", "live/prod", "live/staging"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := FindModuleDirs(fixtureRoot(), tt.ignore)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFindModuleDirsEdgeCases(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, f := range []string{
		"main.tf",
		".terraform/modules/x/main.tf",
		".hidden/main.tf",
		"a/b/c/main.tofu",
		"a/readme.md",
		"data/vars.tfvars",
		"json/x.tf.json",
	} {
		p := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("#"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A directory named like a Terraform file is not a file.
	if err := os.MkdirAll(filepath.Join(root, "weird", "x.tf"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := FindModuleDirs(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".", "a/b/c", "json"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestFindModuleDirsEmptyAndMissing(t *testing.T) {
	t.Parallel()
	got, err := FindModuleDirs(t.TempDir(), nil)
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("empty dir: got %#v, %v; want empty non-nil", got, err)
	}
	if _, err := FindModuleDirs(filepath.Join(t.TempDir(), "missing"), nil); err == nil {
		t.Error("missing root: expected error")
	}
}

func BenchmarkFindModuleDirs(b *testing.B) {
	root := fixtureRoot()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := FindModuleDirs(root, DefaultIgnoreDirs); err != nil {
			b.Fatal(err)
		}
	}
}
