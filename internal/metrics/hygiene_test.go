package metrics

import (
	"context"
	"fmt"
	"math"
	"testing"

	"github.com/rossedman/tfmetrics/internal/config"
	"github.com/rossedman/tfmetrics/internal/facts"
	"github.com/rossedman/tfmetrics/internal/source"
)

func checkByKey(t *testing.T, h HygieneResult, key string) Check {
	t.Helper()
	for _, c := range h.Checks {
		if c.Key == key {
			return c
		}
	}
	t.Fatalf("check %q missing in %+v", key, h.Checks)
	return Check{}
}

func TestCheckRate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		c    Check
		want float64
	}{
		{Check{}, 1},
		{Check{Passed: 0, Total: 4}, 0},
		{Check{Passed: 1, Total: 4}, 0.25},
		{Check{Passed: 4, Total: 4}, 1},
	}
	for _, tt := range tests {
		if got := tt.c.Rate(); got != tt.want {
			t.Errorf("%+v.Rate() = %v, want %v", tt.c, got, tt.want)
		}
	}
}

func TestHygieneChecks(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		repo        facts.ScanResult
		key         string
		passed, tot int
	}{
		{
			"pinned counts remote only",
			facts.ScanResult{Modules: []facts.ModuleFacts{{ModuleCalls: []facts.ModuleCall{
				{SourceKind: source.KindLocal, Pinned: facts.PinnedNone},
				{SourceKind: source.KindRegistry, Pinned: facts.PinnedExact},
				{SourceKind: source.KindRegistry, Pinned: facts.PinnedRange},
				{SourceKind: source.KindGit, Pinned: facts.PinnedNone},
			}}}},
			"pinned", 2, 3,
		},
		{
			"required_version roots only",
			facts.ScanResult{Modules: []facts.ModuleFacts{
				{Kind: facts.KindRoot, RequiredVersion: ">= 1"},
				{Kind: facts.KindRoot},
				{Kind: facts.KindChild},
			}},
			"required_version", 1, 2,
		},
		{
			"required_version falls back to all modules when no roots",
			facts.ScanResult{Modules: []facts.ModuleFacts{
				{Kind: facts.KindChild, RequiredVersion: ">= 1"},
				{Kind: facts.KindChild},
				{Kind: facts.KindChild, RequiredVersion: ">= 2"},
			}},
			"required_version", 2, 3,
		},
		{
			"provider constraints",
			facts.ScanResult{Modules: []facts.ModuleFacts{
				{Providers: []facts.ProviderReq{{Name: "aws", Constraint: "~> 5"}, {Name: "x"}}},
				{Providers: []facts.ProviderReq{{Name: "y", Constraint: ">= 1"}}},
			}},
			"provider_constraints", 2, 3,
		},
		{
			"typed vars",
			facts.ScanResult{Modules: []facts.ModuleFacts{{Variables: []facts.VarFacts{
				{HasType: true}, {HasType: true}, {},
			}}}},
			"typed_vars", 2, 3,
		},
		{
			"described covers vars and outputs",
			facts.ScanResult{Modules: []facts.ModuleFacts{{
				Variables: []facts.VarFacts{{HasDescription: true}, {}},
				Outputs:   []facts.OutputFacts{{HasDescription: true}, {}, {}},
			}}},
			"described", 2, 5,
		},
		{
			"parse_clean counts modules",
			facts.ScanResult{Modules: []facts.ModuleFacts{
				{},
				{Diagnostics: []facts.Diagnostic{{Severity: facts.SeverityError, File: "a.tf"}}},
				{Diagnostics: []facts.Diagnostic{{Severity: facts.SeverityWarning, File: "a.tf"}}},
			}},
			"parse_clean", 2, 3,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := checkByKey(t, Hygiene(tt.repo, nil), tt.key)
			if c.Passed != tt.passed || c.Total != tt.tot {
				t.Errorf("%s = %d/%d, want %d/%d", tt.key, c.Passed, c.Total, tt.passed, tt.tot)
			}
		})
	}
}

func TestHygieneEmptyRepo(t *testing.T) {
	t.Parallel()
	h := Hygiene(facts.ScanResult{}, nil)
	if h.Score != 100 {
		t.Errorf("empty repo score = %v, want 100", h.Score)
	}
	if len(h.Checks) != len(config.DefaultWeights) {
		t.Errorf("checks = %d, want %d", len(h.Checks), len(config.DefaultWeights))
	}
}

func TestHygieneWeights(t *testing.T) {
	t.Parallel()
	// One of two vars typed (typed_vars 50%), everything else passes or is empty.
	r := facts.ScanResult{Modules: []facts.ModuleFacts{{
		Kind: facts.KindRoot, RequiredVersion: ">= 1",
		Variables: []facts.VarFacts{{HasType: true, HasDescription: true}, {HasDescription: true}},
	}}}
	tests := []struct {
		name    string
		weights map[string]float64
		want    float64
	}{
		{"nil uses defaults", nil, 92.5},
		{"empty uses defaults", map[string]float64{}, 92.5},
		{"explicit defaults", config.DefaultWeights, 92.5},
		{"only typed_vars", map[string]float64{"typed_vars": 1}, 50},
		{"zero weight ignored", map[string]float64{"typed_vars": 0, "described": 1}, 100},
		{"custom ratio", map[string]float64{"typed_vars": 1, "described": 3}, 87.5},
		{"all zero is zero", map[string]float64{"typed_vars": 0}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := Hygiene(r, tt.weights).Score; math.Abs(got-tt.want) > 1e-9 {
				t.Errorf("score = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHygieneCheckOrder(t *testing.T) {
	t.Parallel()
	h := Hygiene(facts.ScanResult{}, map[string]float64{"parse_clean": 5, "pinned": 5, "typed_vars": 9})
	var keys []string
	for _, c := range h.Checks {
		keys = append(keys, c.Key)
	}
	// Heaviest first, ties by key; unlisted checks have weight 0 and sort last by key.
	want := []string{"typed_vars", "parse_clean", "pinned", "described", "provider_constraints", "required_version"}
	if fmt.Sprint(keys) != fmt.Sprint(want) {
		t.Errorf("order = %v, want %v", keys, want)
	}
}

func TestHygieneScoreSeriesUsesWeights(t *testing.T) {
	t.Parallel()
	r := facts.ScanResult{Repo: "o/r", Org: "o", Modules: []facts.ModuleFacts{{Kind: facts.KindRoot, Variables: []facts.VarFacts{{}}}}}
	m := Compute(context.Background(), []facts.ScanResult{r}, Options{Weights: map[string]float64{"typed_vars": 1}})
	s, ok := find(m, HygieneScore)
	if !ok || s.Value != 0 {
		t.Errorf("hygiene = %v (found %v), want 0 with custom weights", s.Value, ok)
	}
}
