package sink

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rossedman/tfmetrics/internal/facts"
	"github.com/rossedman/tfmetrics/internal/metrics"
)

var scannedAt = time.Date(2026, 10, 1, 12, 30, 45, 123_000_000, time.UTC)

// fixtureResults is a small hand-built set of facts: one owned repo with a root
// and a child module, and one unowned repo.
func fixtureResults() []facts.ScanResult {
	return []facts.ScanResult{
		{
			SchemaVersion: 1, Repo: "acme/app", Org: "acme", Commit: "abc123", ScannedAt: scannedAt,
			Topics: []string{"terraform"}, Properties: map[string]string{"team": "platform"},
			Owners: []string{"@acme/platform", "@acme/sre"},
			Modules: []facts.ModuleFacts{
				{
					Path: ".", Kind: facts.KindRoot, Files: 3, Lines: 120,
					RequiredVersion: ">= 1.5\nline2", Backend: "s3",
					Providers: []facts.ProviderReq{
						{Name: "aws", Source: "hashicorp/aws", Constraint: "~> 5.0"},
						{Name: "random", Source: "hashicorp/random"},
					},
					Resources:   []facts.BlockRef{{Type: "aws_s3_bucket", Name: "a", Provider: "aws"}, {Type: "aws_sqs_queue", Name: "q", Provider: "aws"}},
					DataSources: []facts.BlockRef{{Type: "aws_caller_identity", Name: "me", Provider: "aws"}},
					ModuleCalls: []facts.ModuleCall{
						{Name: "vpc", Source: "terraform-aws-modules/vpc/aws", SourceKind: "registry", Address: "registry.terraform.io/terraform-aws-modules/vpc/aws", Version: "5.1.0", Pinned: facts.PinnedExact},
						{Name: "net", Source: "git::https://github.com/acme/net.git", SourceKind: "git", Address: "github.com/acme/net", Pinned: facts.PinnedNone, Count: true},
						{Name: "loc", Source: "./child", SourceKind: "local", Address: "./child", Pinned: facts.PinnedNone, ForEach: true},
					},
					Variables: []facts.VarFacts{
						{Name: "a", HasType: true, HasDescription: true},
						{Name: "b"},
						{Name: "c", HasType: true},
					},
					Outputs:     []facts.OutputFacts{{Name: "o1", HasDescription: true}, {Name: "o2"}},
					Lifecycle:   facts.LifecycleFacts{PreventDestroy: 1},
					MovedBlocks: 2, ImportBlocks: 1,
					Diagnostics: []facts.Diagnostic{
						{Severity: facts.SeverityError, Summary: "bad", File: "x.tf", Line: 3},
						{Severity: facts.SeverityError, Summary: "worse", File: "x.tf", Line: 9},
						{Severity: facts.SeverityWarning, Summary: "meh", File: "y.tf"},
					},
				},
				{Path: "child", Kind: facts.KindChild, Files: 1, Lines: 10},
			},
		},
		{
			SchemaVersion: 1, Repo: "acme/lib", Org: "acme", Commit: "def456", ScannedAt: scannedAt,
			Topics: []string{}, Properties: map[string]string{},
			Modules: []facts.ModuleFacts{{Path: ".", Kind: facts.KindRoot, Files: 1, Lines: 5, Backend: "gcs"}},
		},
	}
}

func fixtureMetrics(results []facts.ScanResult) metrics.Metrics {
	return metrics.Compute(context.Background(), results, metrics.Options{
		PropertyLabels: []string{"team"},
		Now:            func() time.Time { return scannedAt },
	})
}

func TestParseSpecs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      []map[string]map[string]any
		want    []Spec
		wantErr string
	}{
		{"nil", nil, nil, ""},
		{"one", []map[string]map[string]any{{"json": {"path": "out"}}}, []Spec{{Name: "json", Options: map[string]any{"path": "out"}}}, ""},
		{"bare name has nil options", []map[string]map[string]any{{"csv": nil}}, []Spec{{Name: "csv"}}, ""},
		{"order kept", []map[string]map[string]any{{"csv": nil}, {"parquet": nil}}, []Spec{{Name: "csv"}, {Name: "parquet"}}, ""},
		{"two keys in one item", []map[string]map[string]any{{"csv": nil, "json": nil}}, nil, "sinks[0]: want exactly one sink name, got 2"},
		{"empty item", []map[string]map[string]any{{"csv": nil}, {}}, nil, "sinks[1]: want exactly one sink name, got 0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseSpecs(tt.in)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %#v want %#v", got, tt.want)
			}
		})
	}
}

func TestNew(t *testing.T) {
	t.Parallel()
	var stdout bytes.Buffer
	env := Env{OutDir: "out", Weights: map[string]float64{"pinned": 2}, Stdout: &stdout}
	tests := []struct {
		name string
		spec Spec
		want Sink
	}{
		{"json default", Spec{Name: "json"}, &JSON{Dir: "out"}},
		{"json path", Spec{Name: "json", Options: map[string]any{"path": "elsewhere"}}, &JSON{Dir: "elsewhere"}},
		{"openmetrics default", Spec{Name: "openmetrics"}, &OpenMetrics{Path: filepath.Join("out", "metrics.prom"), Job: "tfmetrics"}},
		{"openmetrics pushgateway", Spec{Name: "openmetrics", Options: map[string]any{"pushgateway": "http://pg:9091", "job": "tf", "path": "m.prom"}},
			&OpenMetrics{Path: "m.prom", Pushgateway: "http://pg:9091", Job: "tf"}},
		{"otlp default", Spec{Name: "otlp"}, &OTLP{Endpoint: "localhost:4317", Headers: map[string]string{}}},
		{"otlp options", Spec{Name: "otlp", Options: map[string]any{"endpoint": "c:4317", "insecure": true, "headers": map[string]any{"x-key": "v", "n": 3}}},
			&OTLP{Endpoint: "c:4317", Insecure: true, Headers: map[string]string{"x-key": "v", "n": "3"}}},
		{"otlp string bool", Spec{Name: "otlp", Options: map[string]any{"insecure": "true", "headers": map[string]string{"a": "b"}}},
			&OTLP{Endpoint: "localhost:4317", Insecure: true, Headers: map[string]string{"a": "b"}}},
		{"csv", Spec{Name: "csv"}, &Table{Dir: "out", Format: "csv"}},
		{"parquet path", Spec{Name: "parquet", Options: map[string]any{"path": "pq"}}, &Table{Dir: "pq", Format: "parquet"}},
		{"sarif default", Spec{Name: "sarif"}, &SARIF{Dir: filepath.Join("out", "sarif")}},
		{"sarif options", Spec{Name: "sarif", Options: map[string]any{"path": "alerts", "min_behind": 3}}, &SARIF{Dir: "alerts", MinBehind: 3}},
		{"markdown default", Spec{Name: "markdown"}, &Markdown{Path: "-", Weights: env.Weights, Stdout: &stdout}},
		{"markdown options", Spec{Name: "markdown", Options: map[string]any{"path": "r.md", "step_summary": "true"}},
			&Markdown{Path: "r.md", StepSummary: true, Weights: env.Weights, Stdout: &stdout}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := New(tt.spec, env)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %#v want %#v", got, tt.want)
			}
		})
	}
}

func TestNewEveryNameBuilds(t *testing.T) {
	t.Parallel()
	for _, n := range Names {
		s, err := New(Spec{Name: n}, Env{OutDir: t.TempDir()})
		if err != nil || s == nil {
			t.Errorf("New(%q) = %v, %v", n, s, err)
		}
	}
}

func TestNewDefaultsStdout(t *testing.T) {
	t.Parallel()
	s, err := New(Spec{Name: "markdown"}, Env{})
	if err != nil {
		t.Fatal(err)
	}
	if s.(*Markdown).Stdout != os.Stdout {
		t.Error("Stdout should default to os.Stdout")
	}
}

func TestNewUnknown(t *testing.T) {
	t.Parallel()
	_, err := New(Spec{Name: "kafka"}, Env{})
	if err == nil || !strings.Contains(err.Error(), `unknown sink "kafka"`) || !strings.Contains(err.Error(), "parquet") {
		t.Fatalf("err = %v", err)
	}
}

func TestOptions(t *testing.T) {
	t.Parallel()
	o := options{
		"s": "v", "n": 42, "empty": "", "nil": nil,
		"b": true, "bs": "TRUE", "bf": "false", "bad": "maybe", "bn": 1,
		"m1": map[string]any{"a": 1, "b": "x"}, "m2": map[string]string{"c": "d"}, "m3": "nope",
	}
	if got := o.str("s", "d"); got != "v" {
		t.Errorf("str s = %q", got)
	}
	if got := o.str("n", "d"); got != "42" {
		t.Errorf("str n = %q", got)
	}
	for _, k := range []string{"empty", "nil", "missing"} {
		if got := o.str(k, "d"); got != "d" {
			t.Errorf("str %s = %q, want default", k, got)
		}
	}
	boolTests := []struct {
		k    string
		def  bool
		want bool
	}{
		{"b", false, true}, {"bs", false, true}, {"bf", true, false}, {"bad", true, true}, {"bad", false, false},
		{"bn", true, true}, {"missing", true, true}, {"missing", false, false},
	}
	for _, tt := range boolTests {
		if got := o.bool(tt.k, tt.def); got != tt.want {
			t.Errorf("bool(%q,%v) = %v want %v", tt.k, tt.def, got, tt.want)
		}
	}
	if got := o.strMap("m1"); !reflect.DeepEqual(got, map[string]string{"a": "1", "b": "x"}) {
		t.Errorf("strMap m1 = %v", got)
	}
	if got := o.strMap("m2"); !reflect.DeepEqual(got, map[string]string{"c": "d"}) {
		t.Errorf("strMap m2 = %v", got)
	}
	for _, k := range []string{"m3", "missing"} {
		if got := o.strMap(k); got == nil || len(got) != 0 {
			t.Errorf("strMap %s = %#v, want empty non-nil", k, got)
		}
	}
	ints := options{"i": 3, "f": 2.0, "s": " 4 ", "bad": "x", "b": true}
	for k, want := range map[string]int{"i": 3, "f": 2, "s": 4, "bad": 9, "b": 9, "missing": 9} {
		if got := ints.int(k, 9); got != want {
			t.Errorf("int(%q) = %d, want %d", k, got, want)
		}
	}
	var nilOpts options
	if nilOpts.str("x", "d") != "d" || nilOpts.bool("x", true) != true {
		t.Error("nil options should return defaults")
	}
}

func TestCheckLocal(t *testing.T) {
	t.Parallel()
	for _, p := range []string{"out", "/abs/out", "./rel", "C:/x", "-", ""} {
		if err := checkLocal(p); err != nil {
			t.Errorf("checkLocal(%q) = %v", p, err)
		}
	}
	for _, p := range []string{"s3://bucket/key", "gs://b/k", "https://x/y"} {
		err := checkLocal(p)
		if err == nil || !strings.Contains(err.Error(), "only local paths") {
			t.Errorf("checkLocal(%q) = %v", p, err)
		}
	}
}

func TestSinksRejectObjectStorePaths(t *testing.T) {
	t.Parallel()
	results := fixtureResults()
	m := fixtureMetrics(results)
	for _, s := range []Sink{
		&JSON{Dir: "s3://b/out"},
		&OpenMetrics{Path: "s3://b/m.prom"},
		&Table{Dir: "s3://b/t", Format: "csv"},
		&Table{Dir: "gs://b/t", Format: "parquet"},
		&Markdown{Path: "s3://b/r.md", Stdout: &bytes.Buffer{}},
	} {
		err := s.Emit(context.Background(), results, m)
		if err == nil || !strings.Contains(err.Error(), "only local paths") {
			t.Errorf("%T: err = %v", s, err)
		}
	}
}

func TestLabelNamesAndByName(t *testing.T) {
	t.Parallel()
	series := []metrics.Series{
		{Name: "b", Labels: map[string]string{"z": "1", "a": "2"}},
		{Name: "a", Labels: map[string]string{"m": "1"}},
		{Name: "b", Labels: map[string]string{"a": "3"}},
	}
	if got := labelNames(series); !reflect.DeepEqual(got, []string{"a", "m", "z"}) {
		t.Errorf("labelNames = %v", got)
	}
	names, groups := byName(series)
	if !reflect.DeepEqual(names, []string{"a", "b"}) || len(groups["b"]) != 2 || len(groups["a"]) != 1 {
		t.Errorf("byName = %v %v", names, groups)
	}
	if got := labelNames(nil); len(got) != 0 {
		t.Errorf("labelNames(nil) = %v", got)
	}
}
