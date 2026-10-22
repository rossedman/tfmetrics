package facts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func sample(repo string) ScanResult {
	return ScanResult{
		SchemaVersion: SchemaVersion,
		Repo:          repo,
		Org:           "acme",
		Topics:        []string{"terraform"},
		Properties:    map[string]string{"owner": "platform"},
		Commit:        "abc123",
		ScannedAt:     time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Owners:        []string{"platform"},
		Modules: []ModuleFacts{{
			Path: "live/prod", Kind: KindRoot, Files: 1, Lines: 10, Backend: "s3",
			Providers:   []ProviderReq{{Name: "aws", Source: "hashicorp/aws", Constraint: "~> 5.0"}},
			Resources:   []BlockRef{{Type: "aws_instance", Name: "a", Provider: "aws", Count: true}},
			DataSources: []BlockRef{},
			ModuleCalls: []ModuleCall{{Name: "vpc", Source: "../vpc", SourceKind: "local", Address: "modules/vpc"}},
			Variables:   []VarFacts{{Name: "x", HasType: true}},
			Outputs:     []OutputFacts{{Name: "y"}},
			Lifecycle:   LifecycleFacts{PreventDestroy: 2},
			Diagnostics: []Diagnostic{},
		}},
	}
}

func TestFileName(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"acme/net":     "acme__net.json",
		"local/dir":    "local__dir.json",
		"a\\b":         "a__b.json",
		"host:8080/x":  "host_8080__x.json",
		"plain":        "plain.json",
		"":             ".json",
		"org/na.me-x_": "org__na.me-x_.json",
	}
	for in, want := range tests {
		if got := FileName(in); got != want {
			t.Errorf("FileName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWriteRead(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "nested", "out")
	in := sample("acme/net")
	p, err := Write(dir, in)
	if err != nil {
		t.Fatal(err)
	}
	if p != filepath.Join(dir, "acme__net.json") {
		t.Errorf("path = %q", p)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(b), "}\n") || !strings.Contains(string(b), "\n  \"repo\"") {
		t.Errorf("expected indented JSON with trailing newline, got %q", b)
	}
	out, err := Read(p)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Errorf("round trip mismatch:\n in=%+v\nout=%+v", in, out)
	}
}

func TestWriteMkdirError(t *testing.T) {
	t.Parallel()
	f := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(f, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(filepath.Join(f, "sub"), sample("a/b")); err == nil {
		t.Error("expected error writing under a file")
	}
}

func TestRead(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	tests := []struct {
		name    string
		path    string
		wantErr string
	}{
		{"ok", write("ok.json", `{"schema_version":1,"repo":"a/b"}`), ""},
		{"missing", filepath.Join(dir, "missing.json"), "no such file"},
		{"bad json", write("bad.json", `{`), "bad.json"},
		{"schema zero", write("zero.json", `{"repo":"a/b"}`), "unsupported schema_version 0"},
		{"schema 2", write("two.json", `{"schema_version":2,"repo":"a/b"}`), ""},
		{"schema future", write("future.json", `{"schema_version":3,"repo":"a/b"}`), "unsupported schema_version 3"},
		{"schema negative", write("neg.json", `{"schema_version":-1}`), "unsupported schema_version -1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := Read(tt.path)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Errorf("unexpected error %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Errorf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestReadAll(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, r := range []string{"zed/last", "acme/net", "acme/dns"} {
		if _, err := Write(dir, sample(r)); err != nil {
			t.Fatal(err)
		}
	}
	// metrics.json, non-json files and subdirectories are not facts.
	if err := os.WriteFile(filepath.Join(dir, "metrics.json"), []byte(`{"not":"facts"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(filepath.Join(dir, "sub"), sample("deep/repo")); err != nil {
		t.Fatal(err)
	}
	single, err := Write(t.TempDir(), sample("single/file"))
	if err != nil {
		t.Fatal(err)
	}

	got, err := ReadAll([]string{dir, single})
	if err != nil {
		t.Fatal(err)
	}
	var repos []string
	for _, r := range got {
		repos = append(repos, r.Repo)
	}
	want := []string{"acme/dns", "acme/net", "single/file", "zed/last"}
	if !reflect.DeepEqual(repos, want) {
		t.Errorf("repos = %v, want %v", repos, want)
	}

	if out, err := ReadAll(nil); err != nil || out == nil || len(out) != 0 {
		t.Errorf("ReadAll(nil) = %#v, %v", out, err)
	}
}

func TestReadAllErrors(t *testing.T) {
	t.Parallel()
	if _, err := ReadAll([]string{filepath.Join(t.TempDir(), "nope")}); err == nil {
		t.Error("missing path: expected error")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte(`{"schema_version":9}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadAll([]string{dir}); err == nil || !strings.Contains(err.Error(), "schema_version") {
		t.Errorf("err = %v, want schema version error", err)
	}
}

func TestParseErrorFiles(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		diags []Diagnostic
		want  int
	}{
		{"none", nil, 0},
		{"warnings ignored", []Diagnostic{{Severity: SeverityWarning, File: "a.tf"}}, 0},
		{"one error", []Diagnostic{{Severity: SeverityError, File: "a.tf", Line: 1}}, 1},
		{"same file twice", []Diagnostic{{Severity: SeverityError, File: "a.tf", Line: 1}, {Severity: SeverityError, File: "a.tf", Line: 9}}, 1},
		{"two files", []Diagnostic{{Severity: SeverityError, File: "a.tf"}, {Severity: SeverityError, File: "b.tf"}}, 2},
		{"fileless errors each count", []Diagnostic{{Severity: SeverityError}, {Severity: SeverityError}}, 2},
		{"mixed", []Diagnostic{{Severity: SeverityError, File: "a.tf"}, {Severity: SeverityWarning, File: "b.tf"}, {Severity: SeverityError}}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := (ModuleFacts{Diagnostics: tt.diags}).ParseErrorFiles(); got != tt.want {
				t.Errorf("ParseErrorFiles = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestJSONOmitEmpty(t *testing.T) {
	t.Parallel()
	b, err := json.Marshal(BlockRef{Type: "t", Name: "n", Provider: "p"})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"count", "for_each", "prevent_destroy"} {
		if strings.Contains(string(b), k) {
			t.Errorf("%s should be omitted when false: %s", k, b)
		}
	}
}

func BenchmarkWriteRead(b *testing.B) {
	dir := b.TempDir()
	in := sample("acme/net")
	b.ReportAllocs()
	for b.Loop() {
		p, err := Write(dir, in)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := Read(p); err != nil {
			b.Fatal(err)
		}
	}
}
