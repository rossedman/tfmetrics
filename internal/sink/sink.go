// Package sink writes facts and metrics to their destinations. Facts JSON is
// the source of truth; every other sink is generated from it. All sinks share
// one interface.
package sink

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/rossedman/tfmetrics/internal/facts"
	"github.com/rossedman/tfmetrics/internal/metrics"
)

// Sink emits scan results and metrics somewhere.
type Sink interface {
	Emit(ctx context.Context, results []facts.ScanResult, m metrics.Metrics) error
}

// Env is what sinks share: where to write by default, hygiene weights for
// reports, and where "-" writes go.
type Env struct {
	OutDir  string
	Weights map[string]float64
	Stdout  io.Writer
}

// Names lists the available sinks.
var Names = []string{"json", "openmetrics", "otlp", "csv", "parquet", "markdown", "sarif"}

// Spec names a sink and its options.
type Spec struct {
	Name    string
	Options map[string]any
}

// ParseSpecs turns the config's sinks list (each item a one-key map of name to
// options) into specs.
func ParseSpecs(items []map[string]map[string]any) ([]Spec, error) {
	var out []Spec
	for i, item := range items {
		if len(item) != 1 {
			return nil, fmt.Errorf("sinks[%d]: want exactly one sink name, got %d", i, len(item))
		}
		for name, opts := range item {
			out = append(out, Spec{Name: name, Options: opts})
		}
	}
	return out, nil
}

// New builds a sink from a spec.
func New(s Spec, env Env) (Sink, error) {
	if env.Stdout == nil {
		env.Stdout = os.Stdout
	}
	o := options(s.Options)
	switch s.Name {
	case "json":
		return &JSON{Dir: o.str("path", env.OutDir)}, nil
	case "openmetrics":
		return &OpenMetrics{
			Path:        o.str("path", filepath.Join(env.OutDir, "metrics.prom")),
			Pushgateway: o.str("pushgateway", ""),
			Job:         o.str("job", "tfmetrics"),
		}, nil
	case "otlp":
		return &OTLP{
			Endpoint: o.str("endpoint", "localhost:4317"),
			Insecure: o.bool("insecure", false),
			Headers:  o.strMap("headers"),
		}, nil
	case "csv":
		return &Table{Dir: o.str("path", env.OutDir), Format: "csv"}, nil
	case "parquet":
		return &Table{Dir: o.str("path", env.OutDir), Format: "parquet"}, nil
	case "sarif":
		return &SARIF{Dir: o.str("path", filepath.Join(env.OutDir, "sarif")), MinBehind: o.int("min_behind", 0)}, nil
	case "markdown":
		return &Markdown{Path: o.str("path", "-"), StepSummary: o.bool("step_summary", false), Weights: env.Weights, Stdout: env.Stdout}, nil
	}
	return nil, fmt.Errorf("unknown sink %q (have %s)", s.Name, strings.Join(Names, ", "))
}

// options reads typed values from a sink's option map.
type options map[string]any

// str returns a string option or def.
func (o options) str(k, def string) string {
	if v, ok := o[k]; ok && v != nil {
		if s := fmt.Sprint(v); s != "" {
			return s
		}
	}
	return def
}

// int returns an integer option or def.
func (o options) int(k string, def int) int {
	switch v := o[k].(type) {
	case int:
		return v
	case float64:
		return int(v)
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return def
}

// bool returns a boolean option or def.
func (o options) bool(k string, def bool) bool {
	switch v := o[k].(type) {
	case bool:
		return v
	case string:
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

// strMap returns a map option with string values.
func (o options) strMap(k string) map[string]string {
	out := map[string]string{}
	switch v := o[k].(type) {
	case map[string]any:
		for k2, v2 := range v {
			out[k2] = fmt.Sprint(v2)
		}
	case map[string]string:
		for k2, v2 := range v {
			out[k2] = v2
		}
	}
	return out
}

// checkLocal rejects object-store URLs, which sinks do not write to directly.
func checkLocal(p string) error {
	if i := strings.Index(p, "://"); i > 0 {
		return fmt.Errorf("%s: only local paths are supported; write locally and upload (for example with aws s3 cp or gcloud storage cp)", p)
	}
	return nil
}

// labelNames returns the sorted union of label names across series.
func labelNames(series []metrics.Series) []string {
	set := map[string]bool{}
	for _, s := range series {
		for k := range s.Labels {
			set[k] = true
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// byName groups series by metric name, keeping names sorted.
func byName(series []metrics.Series) ([]string, map[string][]metrics.Series) {
	groups := map[string][]metrics.Series{}
	var names []string
	for _, s := range series {
		if _, ok := groups[s.Name]; !ok {
			names = append(names, s.Name)
		}
		groups[s.Name] = append(groups[s.Name], s)
	}
	sort.Strings(names)
	return names, groups
}
