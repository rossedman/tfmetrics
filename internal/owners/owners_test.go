package owners

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func writeFile(t testing.TB, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFromCodeowners(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		path string
		body string
		want []string
	}{
		{"catch-all", ".github/CODEOWNERS", "* @acme/platform\n/modules/ @acme/networking\n", []string{"acme/platform"}},
		{"catch-all after specifics", "CODEOWNERS", "/modules/ @acme/networking\n* @acme/platform\n", []string{"acme/platform"}},
		{"last catch-all wins", "CODEOWNERS", "* @a\n* @b @c\n", []string{"b", "c"}},
		{"slash star forms", "CODEOWNERS", "/* @a\n", []string{"a"}},
		{"double star form", "CODEOWNERS", "/** @z @a\n", []string{"a", "z"}},
		{"no catch-all unions all rules", "CODEOWNERS", "/modules/ @b @a\n*.tf @c\n/docs/ @a\n", []string{"a", "b", "c"}},
		{"comments and blanks", "docs/CODEOWNERS", "# header\n\n* @a # trailing\n   # indented comment\n", []string{"a"}},
		{"emails kept", "CODEOWNERS", "* dev@acme.io @team\n", []string{"dev@acme.io", "team"}},
		{"owners sorted", "CODEOWNERS", "* @zed @amy @mid\n", []string{"amy", "mid", "zed"}},
		{"pattern without owners ignored", "CODEOWNERS", "/modules/\n", []string{}},
		{"duplicate owners in one rule", "CODEOWNERS", "* @a @a @b\n", []string{"a", "b"}},
		{"double @ stripped", "CODEOWNERS", "* @@a\n", []string{"a"}},
		{"bare @ dropped", "CODEOWNERS", "* @ @a\n", []string{"a"}},
		{"empty file", "CODEOWNERS", "", []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeFile(t, root, tt.path, tt.body)
			got := FromCodeowners(root)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestFromCodeownersMissing(t *testing.T) {
	t.Parallel()
	if got := FromCodeowners(t.TempDir()); got != nil {
		t.Errorf("got %#v, want nil", got)
	}
}

func TestFromCodeownersLocationOrder(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "docs/CODEOWNERS", "* @docs\n")
	writeFile(t, root, "CODEOWNERS", "* @root\n")
	if got := FromCodeowners(root); !reflect.DeepEqual(got, []string{"root"}) {
		t.Errorf("root should beat docs: %v", got)
	}
	writeFile(t, root, ".github/CODEOWNERS", "* @gh\n")
	if got := FromCodeowners(root); !reflect.DeepEqual(got, []string{"gh"}) {
		t.Errorf(".github should win: %v", got)
	}
}

func TestResolve(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		codeowners string
		props      map[string]string
		prop       string
		topics     []string
		want       []string
	}{
		{"codeowners beats everything", "* @acme/platform\n", map[string]string{"owner": "x"}, "owner", []string{"team-y"}, []string{"acme/platform"}},
		{"empty codeowners falls through", "# nothing\n", map[string]string{"owner": "x"}, "owner", nil, []string{"x"}},
		{"owner property", "", map[string]string{"owner": "  data-team "}, "owner", []string{"team-y"}, []string{"data-team"}},
		{"custom property name", "", map[string]string{"squad": "blue"}, "squad", nil, []string{"blue"}},
		{"blank property falls to topics", "", map[string]string{"owner": "  "}, "owner", []string{"team-b", "team-a", "other"}, []string{"a", "b"}},
		{"property unset name skips property", "", map[string]string{"owner": "x"}, "", []string{"team-q"}, []string{"q"}},
		{"team topics sorted", "", nil, "owner", []string{"team-zed", "go", "team-amy"}, []string{"amy", "zed"}},
		{"bare team- ignored", "", nil, "owner", []string{"team-"}, []string{Unowned}},
		{"unowned", "", nil, "owner", []string{"terraform"}, []string{Unowned}},
		{"unowned no inputs", "", nil, "", nil, []string{Unowned}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if tt.codeowners != "" {
				writeFile(t, root, "CODEOWNERS", tt.codeowners)
			}
			got := Resolve(root, tt.props, tt.prop, tt.topics)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestResolveFixture(t *testing.T) {
	t.Parallel()
	got := Resolve(filepath.Join("..", "..", "testdata", "repos", "acme-infra"), nil, "owner", nil)
	if !reflect.DeepEqual(got, []string{"acme/platform"}) {
		t.Errorf("got %v", got)
	}
}

func FuzzParseCodeowners(f *testing.F) {
	for _, s := range []string{
		"* @acme/platform\n/modules/ @acme/networking\n",
		"# c\n\n*.tf @a @b # x\n",
		"/** @z @a\n",
		"* dev@acme.io\n",
		"@@@ @@a\n* @@a\n",
		"*\t@a\r\n",
		"",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, content string) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "CODEOWNERS"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		got := FromCodeowners(root)
		if !slices.IsSorted(got) {
			t.Fatalf("not sorted: %q", got)
		}
		for i, o := range got {
			if o == "" {
				t.Fatalf("empty owner in %q", got)
			}
			if i > 0 && got[i-1] == o {
				t.Fatalf("duplicate owner %q in %q", o, got)
			}
			if strings.HasPrefix(o, "@") {
				t.Fatalf("owner with @ prefix %q", o)
			}
			if strings.ContainsAny(o, " \t") {
				t.Fatalf("owner with whitespace %q", o)
			}
		}
		// Resolve always returns something, and the answer is stable.
		r := Resolve(root, nil, "", nil)
		if len(r) == 0 {
			t.Fatal("Resolve returned nothing")
		}
		if len(got) > 0 && !reflect.DeepEqual(r, got) {
			t.Fatalf("Resolve %q != FromCodeowners %q", r, got)
		}
	})
}

func BenchmarkFromCodeowners(b *testing.B) {
	root := b.TempDir()
	writeFile(b, root, ".github/CODEOWNERS", strings.Repeat("/modules/x/ @acme/a @acme/b\n", 200)+"* @acme/platform\n")
	b.ReportAllocs()
	for b.Loop() {
		_ = FromCodeowners(root)
	}
}
