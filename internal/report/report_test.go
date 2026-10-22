package report

import (
	"bytes"
	"context"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	goversion "github.com/hashicorp/go-version"

	"github.com/rossedman/tfmetrics/internal/config"
	"github.com/rossedman/tfmetrics/internal/facts"
	"github.com/rossedman/tfmetrics/internal/scan"
	"github.com/rossedman/tfmetrics/internal/source"
	"github.com/rossedman/tfmetrics/internal/versions"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata/")

const fixtureCommit = "0123456789abcdef0123456789abcdef01234567"

// fixture scans the acme-infra repo with a fixed name, commit and clock.
func fixture(t testing.TB) facts.ScanResult {
	t.Helper()
	// Copy out of the checkout so CODEOWNERS and git state above it are not seen.
	root := t.TempDir()
	if err := os.CopyFS(root, os.DirFS(filepath.Join("..", "..", "testdata", "repos", "acme-infra"))); err != nil {
		t.Fatal(err)
	}
	res, err := scan.Dir(context.Background(), root, scan.Meta{Repo: "acme/infra", Commit: fixtureCommit}, scan.Options{
		Normalizer: source.NewNormalizer([]string{"app.terraform.io"}, nil),
		Now:        func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// fixtureGaps returns gaps for some of the fixture's remote calls: each kind
// of update, one up to date, and the rest unknown.
func fixtureGaps(t testing.TB) Gaps {
	t.Helper()
	eks := versions.Parse([]string{"17.24.0", "17.24.1", "18.0.0", "19.21.0", "19.21.1", "20.0.0", "20.1.0"})
	s3 := versions.Parse([]string{"3.15.0", "3.15.2", "3.16.0"})
	dns := versions.Parse([]string{"1.4.0", "1.4.1"})
	sel := func(c string, avail []*goversion.Version) *goversion.Version {
		v := versions.Selected(c, avail)
		if v == nil {
			t.Fatalf("nothing selected for %s", c)
		}
		return v
	}
	return Gaps{
		GapKey("live/prod", "eks"):       versions.Compare(sel("19.21.0", eks), eks),
		GapKey("live/staging", "eks"):    versions.Compare(sel("17.24.0", eks), eks),
		GapKey("live/prod", "s3_bucket"): versions.Compare(sel("~> 3.15", s3), s3),
		GapKey("live/prod", "dns"):       versions.Compare(sel("1.4.0", dns), dns),
	}
}

func TestUpdates(t *testing.T) {
	t.Parallel()
	u := updates(remoteCalls(fixture(t)), fixtureGaps(t))
	if u != (Updates{Major: 2, Patch: 1, Current: 1}) || u.Outdated() != 3 || u.String() != "2 major, 0 minor, 1 patch" {
		t.Errorf("updates = %+v (%s)", u, u)
	}
	if got := behind(nil, locatedCall{}); got != "-" {
		t.Errorf("unknown gap = %q", got)
	}
	if kind, ok := updateStyle(versions.UpdatePatch); ok {
		t.Errorf("patch styled %v", kind)
	}
}

// golden compares got with testdata/name, rewriting it under -update.
func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from golden (run go test -update to accept)\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func TestGoldenFixture(t *testing.T) {
	t.Parallel()
	r := fixture(t)
	tests := []struct {
		name   string
		file   string
		render func(*bytes.Buffer) error
	}{
		{"text", "acme-infra.txt", func(b *bytes.Buffer) error { return Text(b, r, Options{}) }},
		{"markdown", "acme-infra.md", func(b *bytes.Buffer) error { return Markdown(b, r, Options{}) }},
		{"pretty", "acme-infra-pretty.txt", func(b *bytes.Buffer) error { return Pretty(b, r, Options{}) }},
		{"pretty narrow", "acme-infra-pretty-80.txt", func(b *bytes.Buffer) error { return Pretty(b, r, Options{Width: 80}) }},
		{"text with gaps", "acme-infra-gaps.txt", func(b *bytes.Buffer) error { return Text(b, r, Options{Gaps: fixtureGaps(t)}) }},
		{"markdown with gaps", "acme-infra-gaps.md", func(b *bytes.Buffer) error { return Markdown(b, r, Options{Gaps: fixtureGaps(t)}) }},
		{"pretty with gaps", "acme-infra-pretty-gaps.txt", func(b *bytes.Buffer) error {
			return Pretty(b, r, Options{Gaps: fixtureGaps(t), Width: 100})
		}},
		{"text custom weights", "acme-infra-weights.txt", func(b *bytes.Buffer) error {
			return Text(b, r, Options{Weights: map[string]float64{"pinned": 1, "parse_clean": 1}})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var b bytes.Buffer
			if err := tt.render(&b); err != nil {
				t.Fatal(err)
			}
			golden(t, tt.file, b.Bytes())
		})
	}
}

func TestSummarize(t *testing.T) {
	t.Parallel()
	s := Summarize(fixture(t), nil)
	if s.Roots != 2 || s.Children != 4 {
		t.Errorf("roots/children = %d/%d, want 2/4", s.Roots, s.Children)
	}
	if s.Calls != 9 || s.Unpinned != 1 {
		t.Errorf("calls/unpinned = %d/%d, want 9/1", s.Calls, s.Unpinned)
	}
	if s.ParseErrors != 1 {
		t.Errorf("parse errors = %d, want 1", s.ParseErrors)
	}
	if s.Hygiene.Score <= 0 || s.Hygiene.Score >= 100 {
		t.Errorf("score = %v, want strictly between 0 and 100", s.Hygiene.Score)
	}
}

func TestEmptyRepo(t *testing.T) {
	t.Parallel()
	r := facts.ScanResult{Repo: "o/empty", Commit: "abc"}
	var txt, md bytes.Buffer
	if err := Text(&txt, r, Options{}); err != nil {
		t.Fatal(err)
	}
	if err := Markdown(&md, r, Options{}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"o/empty @ abc", "owners: unowned", "modules      0 (0 root, 0 child)", "hygiene score 100.0 / 100"} {
		if !strings.Contains(txt.String(), want) {
			t.Errorf("text missing %q:\n%s", want, txt.String())
		}
	}
	for _, section := range []string{"\nmodules\n", "remote module calls", "diagnostics"} {
		if strings.Contains(txt.String(), section) {
			t.Errorf("empty text report has section %q", section)
		}
	}
	for _, want := range []string{"### tfmetrics: o/empty", "**Hygiene score: 100.0 / 100**", "| Check | Passed | Weight |"} {
		if !strings.Contains(md.String(), want) {
			t.Errorf("markdown missing %q:\n%s", want, md.String())
		}
	}
	if strings.Contains(md.String(), "<details>") {
		t.Errorf("empty markdown report has details:\n%s", md.String())
	}
}

func TestCommitDisplay(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, commit, want string }{
		{"long trimmed to 12", fixtureCommit, "o/r @ 0123456789ab\n"},
		{"exactly 12", "0123456789ab", "o/r @ 0123456789ab\n"},
		{"short kept", "abc123", "o/r @ abc123\n"},
		{"none", "", "o/r @ (not a git checkout)\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var b bytes.Buffer
			if err := Text(&b, facts.ScanResult{Repo: "o/r", Commit: tt.commit}, Options{}); err != nil {
				t.Fatal(err)
			}
			if first, _, _ := strings.Cut(b.String(), "owners:"); first != tt.want {
				t.Errorf("header = %q, want %q", first, tt.want)
			}
		})
	}
}

func TestDiagnosticLocations(t *testing.T) {
	t.Parallel()
	r := facts.ScanResult{Repo: "o/r", Modules: []facts.ModuleFacts{{
		Path: "m", Kind: facts.KindRoot,
		Diagnostics: []facts.Diagnostic{
			{Severity: facts.SeverityError, File: "a.tf", Line: 3, Summary: "with line"},
			{Severity: facts.SeverityWarning, File: "b.tf", Summary: "no line"},
			{Severity: facts.SeverityError, Summary: "no file"},
		},
	}}}
	var txt, md bytes.Buffer
	_ = Text(&txt, r, Options{})
	_ = Markdown(&md, r, Options{})
	for _, want := range []string{"error a.tf:3: with line", "warning b.tf: no line", "error -: no file"} {
		if !strings.Contains(txt.String(), want) {
			t.Errorf("text missing %q:\n%s", want, txt.String())
		}
	}
	for _, want := range []string{"- **error** `a.tf:3`: with line", "- **warning** `b.tf`: no line", "- **error** `-`: no file"} {
		if !strings.Contains(md.String(), want) {
			t.Errorf("markdown missing %q:\n%s", want, md.String())
		}
	}
}

func TestRemoteCallsSortedAndDashes(t *testing.T) {
	t.Parallel()
	r := facts.ScanResult{Repo: "o/r", Modules: []facts.ModuleFacts{{
		Path: "m", Kind: facts.KindRoot,
		ModuleCalls: []facts.ModuleCall{
			{Address: "z/mod", SourceKind: source.KindGit, Pinned: facts.PinnedNone},
			{Address: "a/mod", SourceKind: source.KindRegistry, Version: "1.0.0", Pinned: facts.PinnedExact},
			{Address: "../local", SourceKind: source.KindLocal},
		},
	}}}
	var txt, md bytes.Buffer
	_ = Text(&txt, r, Options{})
	_ = Markdown(&md, r, Options{})
	if strings.Contains(txt.String(), "../local") || strings.Contains(md.String(), "../local") {
		t.Error("local call listed as remote")
	}
	if strings.Index(txt.String(), "a/mod") > strings.Index(txt.String(), "z/mod") {
		t.Errorf("text calls not sorted by address:\n%s", txt.String())
	}
	if !strings.Contains(md.String(), "| `z/mod` | — | none | `m` |") {
		t.Errorf("markdown missing em dash row:\n%s", md.String())
	}
	if !strings.Contains(txt.String(), "z/mod") || !strings.Contains(txt.String(), "-  ") {
		t.Errorf("text missing dash:\n%s", txt.String())
	}
}

func TestDashHelpers(t *testing.T) {
	t.Parallel()
	if dash(" ") != "-" || dash("x") != "x" {
		t.Error("dash")
	}
	if mdDash("") != "—" || mdDash("x") != "`x`" {
		t.Error("mdDash")
	}
}

func TestCheckNamesCoverDefaults(t *testing.T) {
	t.Parallel()
	for k := range config.DefaultWeights {
		if checkNames[k] == "" {
			t.Errorf("no display name for hygiene check %q", k)
		}
	}
}

type failWriter struct{ after int }

func (f *failWriter) Write(p []byte) (int, error) {
	if f.after <= 0 {
		return 0, os.ErrClosed
	}
	f.after--
	return len(p), nil
}

func TestWriteErrors(t *testing.T) {
	t.Parallel()
	r := fixture(t)
	// tabwriter buffers, so failures surface at Flush; Markdown's last write errors directly.
	if err := Text(&failWriter{}, r, Options{}); err == nil {
		t.Error("Text: want error from failing writer")
	}
	if err := Markdown(&failWriter{after: 1 << 20}, r, Options{}); err != nil {
		t.Errorf("Markdown with healthy writer: %v", err)
	}
	// Markdown reports only the final write error; find the count of writes then fail the last.
	var n countWriter
	if err := Markdown(&n, r, Options{}); err != nil {
		t.Fatal(err)
	}
	if err := Markdown(&failWriter{after: n.writes - 1}, r, Options{}); err == nil {
		t.Error("Markdown: want error when final write fails")
	}
	// Each tabwriter flush in Text can fail independently.
	var tn countWriter
	if err := Text(&tn, r, Options{}); err != nil {
		t.Fatal(err)
	}
	for after := 0; after < tn.writes; after++ {
		_ = Text(&failWriter{after: after}, r, Options{})
	}
}

type countWriter struct{ writes int }

func (c *countWriter) Write(p []byte) (int, error) { c.writes++; return len(p), nil }

func TestFitColumns(t *testing.T) {
	t.Parallel()
	headers := []string{"Address", "Kind", "In"}
	rows := [][]string{
		{"registry.terraform.io/terraform-aws-modules/eks/aws", "registry", "infra/aws/prod/cluster"},
		{"x", "git", "y"},
	}
	if got := fitColumns(headers, rows, 0); !slices.Equal(got, []int{51, 8, 22}) {
		t.Errorf("no limit = %v", got)
	}
	// 60 cells leave 60-1-9 = 50 for content: the widest column gives way
	// first, and the short Kind column keeps its width.
	got := fitColumns(headers, rows, 60)
	if !slices.Equal(got, []int{21, 8, 21}) {
		t.Errorf("width 60 = %v", got)
	}
	// Too narrow for anything: every column stops at its floor.
	if got := fitColumns(headers, rows, 10); !slices.Equal(got, []int{8, 8, 8}) {
		t.Errorf("width 10 = %v", got)
	}
}

func TestPrettyMarksPinningAndDiagnostics(t *testing.T) {
	t.Parallel()
	r := facts.ScanResult{Repo: "acme/x", Modules: []facts.ModuleFacts{{
		Path: ".", Kind: facts.KindRoot,
		ModuleCalls: []facts.ModuleCall{
			{Name: "a", SourceKind: source.KindRegistry, Address: "r/a", Version: "1.0.0", Pinned: facts.PinnedExact},
			{Name: "b", SourceKind: source.KindGit, Address: "g/b", Version: "main", Pinned: facts.PinnedRange},
			{Name: "c", SourceKind: source.KindGit, Address: "g/c", Pinned: facts.PinnedNone},
		},
		Diagnostics: []facts.Diagnostic{
			{Severity: facts.SeverityError, Summary: "Duplicate resource", File: "main.tf", Line: 3},
			{Severity: facts.SeverityWarning, Summary: "OpenTofu file not parsed", File: "x.tofu"},
		},
	}}}
	var b bytes.Buffer
	if err := Pretty(&b, r, Options{}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"not a git checkout", "✗ Duplicate resource  main.tf:3", "! OpenTofu file not parsed  x.tofu", "│ exact", "│ range", "│ none"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("missing %q in:\n%s", want, b.String())
		}
	}
}
