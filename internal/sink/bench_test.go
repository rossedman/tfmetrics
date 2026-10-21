package sink

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/rossedman/tfmetrics/internal/facts"
)

// benchResults replicates the fixture into n repos.
func benchResults(n int) []facts.ScanResult {
	base := fixtureResults()
	out := make([]facts.ScanResult, 0, n)
	for i := range n {
		r := base[i%len(base)]
		r.Repo = fmt.Sprintf("acme/repo-%04d", i)
		out = append(out, r)
	}
	return out
}

func BenchmarkRows(b *testing.B) {
	results := benchResults(500)
	b.ReportAllocs()
	for b.Loop() {
		mods, calls := Rows(results)
		if len(mods) == 0 || len(calls) == 0 {
			b.Fatal("no rows")
		}
	}
}

func BenchmarkOpenMetricsEncode(b *testing.B) {
	m := fixtureMetrics(benchResults(500))
	path := filepath.Join(b.TempDir(), "metrics.prom")
	om := &OpenMetrics{Path: path}
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		if err := om.Emit(ctx, nil, m); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRegistryGather(b *testing.B) {
	m := fixtureMetrics(benchResults(500))
	b.ReportAllocs()
	for b.Loop() {
		reg, err := Registry(m)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := reg.Gather(); err != nil {
			b.Fatal(err)
		}
	}
}
