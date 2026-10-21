package metrics

import (
	"math"
	"sort"

	"github.com/rossedman/tfmetrics/internal/config"
	"github.com/rossedman/tfmetrics/internal/facts"
	"github.com/rossedman/tfmetrics/internal/source"
)

// Check is one hygiene check's pass rate.
type Check struct {
	Key    string  `json:"key"`
	Weight float64 `json:"weight"`
	Passed int     `json:"passed"`
	Total  int     `json:"total"`
}

// Rate returns the pass rate, treating a check with nothing to check as passed.
func (c Check) Rate() float64 {
	if c.Total == 0 {
		return 1
	}
	return float64(c.Passed) / float64(c.Total)
}

// HygieneResult is a repo's score and the checks behind it.
type HygieneResult struct {
	Score  float64 `json:"score"`
	Checks []Check `json:"checks"`
}

// Hygiene scores a repo 0-100 as a weighted sum of pass rates:
//
//   - pinned: remote module calls with a version or ref
//   - required_version: root modules (or all modules, if none are roots) setting required_version
//   - provider_constraints: provider requirements with a version constraint
//   - typed_vars: variables with a type
//   - described: variables and outputs with a description
//   - parse_clean: modules without parse errors
//
// Nil or empty weights use the defaults.
func Hygiene(r facts.ScanResult, weights map[string]float64) HygieneResult {
	if len(weights) == 0 {
		weights = config.DefaultWeights
	}
	c := map[string]*Check{}
	for k := range config.DefaultWeights {
		c[k] = &Check{Key: k, Weight: weights[k]}
	}
	roots := 0
	for _, m := range r.Modules {
		if m.Kind == facts.KindRoot {
			roots++
		}
	}
	for _, m := range r.Modules {
		for _, call := range m.ModuleCalls {
			if call.SourceKind == source.KindLocal {
				continue
			}
			c["pinned"].Total++
			if call.Pinned != facts.PinnedNone {
				c["pinned"].Passed++
			}
		}
		if roots == 0 || m.Kind == facts.KindRoot {
			c["required_version"].Total++
			if m.RequiredVersion != "" {
				c["required_version"].Passed++
			}
		}
		for _, p := range m.Providers {
			c["provider_constraints"].Total++
			if p.Constraint != "" {
				c["provider_constraints"].Passed++
			}
		}
		for _, v := range m.Variables {
			c["typed_vars"].Total++
			if v.HasType {
				c["typed_vars"].Passed++
			}
			c["described"].Total++
			if v.HasDescription {
				c["described"].Passed++
			}
		}
		for _, o := range m.Outputs {
			c["described"].Total++
			if o.HasDescription {
				c["described"].Passed++
			}
		}
		c["parse_clean"].Total++
		if m.ParseErrorFiles() == 0 {
			c["parse_clean"].Passed++
		}
	}
	var sum, wsum float64
	out := HygieneResult{}
	for _, chk := range c {
		sum += chk.Weight * chk.Rate()
		wsum += chk.Weight
		out.Checks = append(out.Checks, *chk)
	}
	sort.Slice(out.Checks, func(i, j int) bool {
		if out.Checks[i].Weight != out.Checks[j].Weight {
			return out.Checks[i].Weight > out.Checks[j].Weight
		}
		return out.Checks[i].Key < out.Checks[j].Key
	})
	if wsum > 0 {
		out.Score = math.Round(sum/wsum*1000) / 10
	}
	return out
}
