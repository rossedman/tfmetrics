package metrics

import (
	"context"
	"fmt"
	"testing"

	"github.com/rossedman/tfmetrics/internal/facts"
)

// synthetic builds n repos across 10 orgs, each with several modules.
func synthetic(n int) []facts.ScanResult {
	out := make([]facts.ScanResult, n)
	for i := range out {
		r := repoA()
		r.Repo = fmt.Sprintf("org%d/repo%d", i%10, i)
		r.Org = fmt.Sprintf("org%d", i%10)
		if i%3 == 0 {
			r.Owners = nil
		}
		for j := range r.Modules {
			r.Modules[j].Path = fmt.Sprintf("m%d", j)
			for k := range r.Modules[j].Resources {
				r.Modules[j].Resources[k].Type = fmt.Sprintf("aws_thing_%d", (i+k)%25)
			}
		}
		out[i] = r
	}
	return out
}

func BenchmarkCompute(b *testing.B) {
	in := synthetic(200)
	opts := Options{PropertyLabels: []string{"team"}}
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		Compute(ctx, in, opts)
	}
}

func BenchmarkComputeWithRollup(b *testing.B) {
	in := synthetic(200)
	opts := Options{DropLabels: []string{"resource_type", "repo"}}
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		Compute(ctx, in, opts)
	}
}

func BenchmarkHygiene(b *testing.B) {
	r := repoA()
	b.ReportAllocs()
	for b.Loop() {
		Hygiene(r, nil)
	}
}
