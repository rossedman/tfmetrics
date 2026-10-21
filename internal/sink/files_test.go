package sink

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/parquet-go/parquet-go"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"

	"github.com/rossedman/tfmetrics/internal/facts"
	"github.com/rossedman/tfmetrics/internal/metrics"
)

func TestJSONSink(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "run")
	results := fixtureResults()
	m := fixtureMetrics(results)
	if err := (&JSON{Dir: dir}).Emit(context.Background(), results, m); err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		got, err := facts.Read(filepath.Join(dir, "facts", facts.FileName(r.Repo)))
		if err != nil {
			t.Fatal(err)
		}
		if got.Repo != r.Repo || got.Commit != r.Commit || len(got.Modules) != len(r.Modules) {
			t.Errorf("facts for %s = %+v", r.Repo, got)
		}
	}
	b, err := os.ReadFile(filepath.Join(dir, "metrics.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(b, []byte("}\n")) {
		t.Error("metrics.json should end with a newline")
	}
	var got metrics.Metrics
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Repos != m.Repos || len(got.Series) != len(m.Series) {
		t.Errorf("metrics round trip: repos %d/%d series %d/%d", got.Repos, m.Repos, len(got.Series), len(m.Series))
	}
}

func TestJSONSinkWriteError(t *testing.T) {
	t.Parallel()
	blocker := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	err := (&JSON{Dir: filepath.Join(blocker, "x")}).Emit(context.Background(), fixtureResults(), metrics.Metrics{})
	if err == nil {
		t.Fatal("want error")
	}
	// No results: metrics.json write itself fails.
	err = (&JSON{Dir: filepath.Join(blocker, "y")}).Emit(context.Background(), nil, metrics.Metrics{})
	if err == nil {
		t.Fatal("want error")
	}
}

func parseExposition(t *testing.T, b []byte) map[string]*dto.MetricFamily {
	t.Helper()
	p := expfmt.NewTextParser(model.UTF8Validation)
	mfs, err := p.TextToMetricFamilies(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, b)
	}
	return mfs
}

func labelsOf(m *dto.Metric) map[string]string {
	out := map[string]string{}
	for _, l := range m.Label {
		out[l.GetName()] = l.GetValue()
	}
	return out
}

func TestOpenMetricsFile(t *testing.T) {
	t.Parallel()
	results := fixtureResults()
	m := fixtureMetrics(results)
	path := filepath.Join(t.TempDir(), "nested", "metrics.prom")
	if err := (&OpenMetrics{Path: path}).Emit(context.Background(), results, m); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(b, []byte("# EOF\n")) {
		t.Errorf("missing # EOF terminator; tail: %q", b[max(0, len(b)-40):])
	}
	mfs := parseExposition(t, b)
	for _, n := range []string{metrics.ReposTotal, metrics.ModulesTotal, metrics.ResourcesDeclared, metrics.HygieneScore} {
		mf, ok := mfs[n]
		if !ok {
			t.Errorf("metric %s missing", n)
			continue
		}
		if mf.GetType() != dto.MetricType_GAUGE {
			t.Errorf("%s type = %v", n, mf.GetType())
		}
		if mf.GetHelp() != metrics.Help[n] {
			t.Errorf("%s help = %q", n, mf.GetHelp())
		}
	}
	// Every computed series round-trips with its labels and value.
	for _, want := range m.Series {
		mf := mfs[want.Name]
		if mf == nil {
			t.Fatalf("family %s missing", want.Name)
		}
		found := false
		for _, got := range mf.Metric {
			ls := labelsOf(got)
			match := got.GetGauge().GetValue() == want.Value
			for k, v := range want.Labels {
				if ls[k] != v {
					match = false
				}
			}
			if match {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("series %s%v = %v not in output", want.Name, want.Labels, want.Value)
		}
	}
}

func TestOpenMetricsFileLabelsSparse(t *testing.T) {
	t.Parallel()
	// Series of one metric with different label sets share one Desc; missing
	// labels are empty values.
	m := metrics.Metrics{Series: []metrics.Series{
		{Name: "tf_x", Labels: map[string]string{"a": "1"}, Value: 1},
		{Name: "tf_x", Labels: map[string]string{"b": "2"}, Value: 2},
	}}
	path := filepath.Join(t.TempDir(), "m.prom")
	if err := (&OpenMetrics{Path: path}).Emit(context.Background(), nil, m); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	mf := parseExposition(t, b)["tf_x"]
	if mf == nil || len(mf.Metric) != 2 {
		t.Fatalf("got %v\n%s", mf, b)
	}
}

func TestOpenMetricsEmpty(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "m.prom")
	if err := (&OpenMetrics{Path: path}).Emit(context.Background(), nil, metrics.Metrics{}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if strings.TrimSpace(string(b)) != "# EOF" {
		t.Errorf("empty metrics file = %q", b)
	}
}

func TestOpenMetricsFileErrors(t *testing.T) {
	t.Parallel()
	blocker := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	m := fixtureMetrics(fixtureResults())
	if err := (&OpenMetrics{Path: filepath.Join(blocker, "sub", "m.prom")}).Emit(context.Background(), nil, m); err == nil {
		t.Error("want mkdir error")
	}
	// Path is a directory: create fails.
	if err := (&OpenMetrics{Path: t.TempDir()}).Emit(context.Background(), nil, m); err == nil {
		t.Error("want create error")
	}
}

func TestOpenMetricsInconsistentSeriesFailsGather(t *testing.T) {
	t.Parallel()
	// Two identical label sets produce duplicate metrics; Gather rejects them.
	m := metrics.Metrics{Series: []metrics.Series{
		{Name: "tf_dup", Labels: map[string]string{"a": "1"}, Value: 1},
		{Name: "tf_dup", Labels: map[string]string{"a": "1"}, Value: 2},
	}}
	err := (&OpenMetrics{Path: filepath.Join(t.TempDir(), "m.prom")}).Emit(context.Background(), nil, m)
	if err == nil {
		t.Fatal("want gather error for duplicate series")
	}
}

func TestPushgateway(t *testing.T) {
	t.Parallel()
	type req struct {
		method, path, ctype string
		body                []byte
	}
	var mu sync.Mutex
	var got []req
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, req{r.Method, r.URL.Path, r.Header.Get("Content-Type"), b})
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	results := fixtureResults()
	m := fixtureMetrics(results)
	om := &OpenMetrics{Pushgateway: srv.URL, Job: "tf-nightly", Path: "s3://ignored/when/pushing"}
	if err := om.Emit(context.Background(), results, m); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("requests = %d", len(got))
	}
	r := got[0]
	if r.method != http.MethodPut && r.method != http.MethodPost {
		t.Errorf("method = %s", r.method)
	}
	if r.path != "/metrics/job/tf-nightly" {
		t.Errorf("path = %s", r.path)
	}
	dec := expfmt.NewDecoder(bytes.NewReader(r.body), expfmt.Format(r.ctype))
	names := map[string]int{}
	for {
		var mf dto.MetricFamily
		if err := dec.Decode(&mf); err != nil {
			if err != io.EOF {
				t.Fatalf("decode: %v (content-type %q)", err, r.ctype)
			}
			break
		}
		names[mf.GetName()] = len(mf.Metric)
	}
	if names[metrics.ReposTotal] != 2 || names[metrics.ModulesTotal] == 0 {
		t.Errorf("pushed families = %v", names)
	}
}

func TestPushgatewayError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer srv.Close()
	err := (&OpenMetrics{Pushgateway: srv.URL, Job: "j"}).Emit(context.Background(), nil, fixtureMetrics(fixtureResults()))
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("err = %v", err)
	}
}

func TestRows(t *testing.T) {
	t.Parallel()
	mods, calls := Rows(fixtureResults())
	if len(mods) != 3 || len(calls) != 3 {
		t.Fatalf("mods=%d calls=%d", len(mods), len(calls))
	}
	root := mods[0]
	want := ModuleRow{
		Repo: "acme/app", Org: "acme", Owner: "@acme/platform,@acme/sre", Commit: "abc123", ScannedAt: scannedAt,
		Path: ".", Kind: "root", Files: 3, Lines: 120, RequiredVersion: ">= 1.5\nline2", Backend: "s3",
		Providers: 2, ProvidersConstraint: 1, Resources: 2, DataSources: 1, ModuleCalls: 3,
		Variables: 3, VariablesUntyped: 1, VariablesUndescr: 2, Outputs: 2, OutputsUndescribed: 1,
		PreventDestroy: 1, MovedBlocks: 2, ImportBlocks: 1, ParseErrors: 1,
	}
	if root != want {
		t.Errorf("root row\n got %+v\nwant %+v", root, want)
	}
	if mods[1].Path != "child" || mods[1].Kind != "child" || mods[2].Owner != "unowned" {
		t.Errorf("rows: %+v %+v", mods[1], mods[2])
	}
	c := calls[1]
	if c.ModulePath != "." || c.Name != "net" || c.SourceKind != "git" || c.Pinned != "none" || !c.Count || c.ForEach || c.Owner != "@acme/platform,@acme/sre" {
		t.Errorf("call row = %+v", c)
	}
	if !calls[2].ForEach {
		t.Errorf("call row = %+v", calls[2])
	}
	if m, c := Rows(nil); len(m) != 0 || len(c) != 0 {
		t.Errorf("Rows(nil) = %v %v", m, c)
	}
}

func readCSV(t *testing.T, path string) [][]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	recs, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

func TestCSVSink(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "csv")
	if err := (&Table{Dir: dir, Format: "csv"}).Emit(context.Background(), fixtureResults(), metrics.Metrics{}); err != nil {
		t.Fatal(err)
	}
	mods := readCSV(t, filepath.Join(dir, "modules.csv"))
	if len(mods) != 4 { // header + 3
		t.Fatalf("modules rows = %d", len(mods))
	}
	wantHeader := []string{"repo", "org", "owner", "commit", "scanned_at", "path", "kind", "files", "lines", "required_version", "backend",
		"providers", "providers_constrained", "resources", "data_sources", "module_calls", "variables", "variables_untyped",
		"variables_undescribed", "outputs", "outputs_undescribed", "prevent_destroy", "moved_blocks", "import_blocks", "parse_errors"}
	if !slices.Equal(mods[0], wantHeader) {
		t.Errorf("header = %v", mods[0])
	}
	wantRow := []string{"acme/app", "acme", "@acme/platform,@acme/sre", "abc123", "2026-10-01T12:30:45Z", ".", "root", "3", "120", ">= 1.5 line2", "s3",
		"2", "1", "2", "1", "3", "3", "1", "2", "2", "1", "1", "2", "1", "1"}
	if !slices.Equal(mods[1], wantRow) {
		t.Errorf("row =\n%v\nwant\n%v", mods[1], wantRow)
	}
	calls := readCSV(t, filepath.Join(dir, "module_calls.csv"))
	if len(calls) != 4 || calls[0][5] != "module_path" {
		t.Fatalf("calls = %v", calls)
	}
	last := calls[3]
	if last[6] != "loc" || last[len(last)-1] != "true" || last[len(last)-2] != "false" {
		t.Errorf("call row = %v", last)
	}
}

func TestCSVEmptyResultsWritesHeaders(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := (&Table{Dir: dir, Format: "csv"}).Emit(context.Background(), nil, metrics.Metrics{}); err != nil {
		t.Fatal(err)
	}
	if recs := readCSV(t, filepath.Join(dir, "modules.csv")); len(recs) != 1 {
		t.Errorf("records = %d", len(recs))
	}
}

func TestParquetSink(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "pq")
	results := fixtureResults()
	if err := (&Table{Dir: dir, Format: "parquet"}).Emit(context.Background(), results, metrics.Metrics{}); err != nil {
		t.Fatal(err)
	}
	wantMods, wantCalls := Rows(results)
	gotMods, err := parquet.ReadFile[ModuleRow](filepath.Join(dir, "modules.parquet"))
	if err != nil {
		t.Fatal(err)
	}
	if len(gotMods) != len(wantMods) {
		t.Fatalf("module rows = %d want %d", len(gotMods), len(wantMods))
	}
	for i := range wantMods {
		g, w := gotMods[i], wantMods[i]
		if !g.ScannedAt.Equal(w.ScannedAt) {
			t.Errorf("row %d scanned_at = %v want %v", i, g.ScannedAt, w.ScannedAt)
		}
		g.ScannedAt = w.ScannedAt
		if g != w {
			t.Errorf("row %d\n got %+v\nwant %+v", i, g, w)
		}
	}
	gotCalls, err := parquet.ReadFile[CallRow](filepath.Join(dir, "module_calls.parquet"))
	if err != nil {
		t.Fatal(err)
	}
	if len(gotCalls) != len(wantCalls) {
		t.Fatalf("call rows = %d want %d", len(gotCalls), len(wantCalls))
	}
	for i := range wantCalls {
		g, w := gotCalls[i], wantCalls[i]
		g.ScannedAt = w.ScannedAt
		if g != w {
			t.Errorf("call %d\n got %+v\nwant %+v", i, g, w)
		}
	}
}

func TestTableErrors(t *testing.T) {
	t.Parallel()
	blocker := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := (&Table{Dir: t.TempDir(), Format: "xml"}).Emit(ctx, nil, metrics.Metrics{}); err == nil || !strings.Contains(err.Error(), "unknown table format") {
		t.Errorf("err = %v", err)
	}
	for _, f := range []string{"csv", "parquet"} {
		if err := (&Table{Dir: filepath.Join(blocker, "x"), Format: f}).Emit(ctx, nil, metrics.Metrics{}); err == nil {
			t.Errorf("%s: want mkdir error", f)
		}
	}
	// Second file can't be created: a directory squats on its name.
	for f, name := range map[string]string{"csv": "module_calls.csv", "parquet": "module_calls.parquet"} {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := (&Table{Dir: dir, Format: f}).Emit(ctx, fixtureResults(), metrics.Metrics{}); err == nil {
			t.Errorf("%s: want error writing %s", f, name)
		}
	}
	// First file can't be created.
	for f, name := range map[string]string{"csv": "modules.csv", "parquet": "modules.parquet"} {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := (&Table{Dir: dir, Format: f}).Emit(ctx, fixtureResults(), metrics.Metrics{}); err == nil {
			t.Errorf("%s: want error writing %s", f, name)
		}
	}
}

func TestMarkdownToFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "report.md")
	var stdout bytes.Buffer
	md := &Markdown{Path: path, Stdout: &stdout}
	if err := md.Emit(context.Background(), fixtureResults(), metrics.Metrics{}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	s := string(b)
	for _, want := range []string{"### tfmetrics: acme/app", "### tfmetrics: acme/lib", "Hygiene score", "| Check | Passed | Weight |", "Remote module calls"} {
		if !strings.Contains(s, want) {
			t.Errorf("report missing %q", want)
		}
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout should be untouched when writing a file, got %q", stdout.String())
	}
}

func TestMarkdownToStdout(t *testing.T) {
	t.Parallel()
	for _, p := range []string{"-", ""} {
		var stdout bytes.Buffer
		md := &Markdown{Path: p, Stdout: &stdout}
		if err := md.Emit(context.Background(), fixtureResults()[:1], metrics.Metrics{}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(stdout.String(), "### tfmetrics: acme/app") || strings.Contains(stdout.String(), "acme/lib") {
			t.Errorf("path %q: stdout = %q", p, stdout.String())
		}
	}
}

func TestMarkdownStepSummary(t *testing.T) {
	summary := filepath.Join(t.TempDir(), "summary.md")
	if err := os.WriteFile(summary, []byte("existing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GITHUB_STEP_SUMMARY", summary)

	var stdout bytes.Buffer
	if err := (&Markdown{Path: "-", StepSummary: true, Stdout: &stdout}).Emit(context.Background(), fixtureResults(), metrics.Metrics{}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(summary)
	if !strings.HasPrefix(string(b), "existing\n") || !strings.Contains(string(b), "### tfmetrics: acme/lib") {
		t.Errorf("summary not appended: %q", b)
	}
	if !strings.Contains(stdout.String(), "### tfmetrics: acme/app") {
		t.Error("stdout should still receive the report")
	}
	// Appending twice keeps both.
	if err := (&Markdown{Path: "-", StepSummary: true, Stdout: io.Discard}).Emit(context.Background(), fixtureResults(), metrics.Metrics{}); err != nil {
		t.Fatal(err)
	}
	b2, _ := os.ReadFile(summary)
	if strings.Count(string(b2), "### tfmetrics: acme/app") != 2 {
		t.Errorf("want two appended reports")
	}

	// Disabled: env set but option off leaves the file alone.
	before := string(b2)
	if err := (&Markdown{Path: "-", Stdout: io.Discard}).Emit(context.Background(), fixtureResults(), metrics.Metrics{}); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(summary); string(after) != before {
		t.Error("step summary written without step_summary option")
	}
}

func TestMarkdownStepSummaryUnsetOrBad(t *testing.T) {
	t.Setenv("GITHUB_STEP_SUMMARY", "")
	if err := (&Markdown{Path: "-", StepSummary: true, Stdout: io.Discard}).Emit(context.Background(), fixtureResults(), metrics.Metrics{}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GITHUB_STEP_SUMMARY", filepath.Join(t.TempDir(), "no", "such", "dir", "s.md"))
	if err := (&Markdown{Path: "-", StepSummary: true, Stdout: io.Discard}).Emit(context.Background(), fixtureResults(), metrics.Metrics{}); err == nil {
		t.Fatal("want error opening step summary")
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestMarkdownErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	if err := (&Markdown{Path: filepath.Join(t.TempDir(), "no", "dir", "r.md"), Stdout: io.Discard}).Emit(ctx, fixtureResults(), metrics.Metrics{}); err == nil {
		t.Error("want create error")
	}
	if err := (&Markdown{Path: "-", Stdout: failWriter{}}).Emit(ctx, fixtureResults(), metrics.Metrics{}); err == nil {
		t.Error("want write error")
	}
}

func TestSinkInterface(t *testing.T) {
	t.Parallel()
	var _ Sink = (*JSON)(nil)
	var _ Sink = (*OpenMetrics)(nil)
	var _ Sink = (*OTLP)(nil)
	var _ Sink = (*Table)(nil)
	var _ Sink = (*Markdown)(nil)
}
