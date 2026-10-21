// Package metrics computes the metrics catalog from facts. Every metric is a
// gauge prefixed tf_, labelled with repo, owner and org (plus any promoted
// custom properties) unless noted.
package metrics

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/rossedman/tfmetrics/internal/facts"
	"github.com/rossedman/tfmetrics/internal/source"
	"github.com/rossedman/tfmetrics/internal/versions"
)

// Metric names.
const (
	ReposTotal             = "tf_repos_total"
	ModulesTotal           = "tf_modules_total"
	ResourcesDeclared      = "tf_resources_declared"
	DataSourcesDeclared    = "tf_data_sources_declared"
	ModuleCalls            = "tf_module_calls"
	ModuleVersionsBehind   = "tf_module_versions_behind"
	ModuleCallsUnpinned    = "tf_module_calls_unpinned"
	ProviderConstraint     = "tf_provider_constraint"
	RequiredVersion        = "tf_required_version"
	Backend                = "tf_backend"
	VariablesUntyped       = "tf_variables_untyped"
	VariablesUndescribed   = "tf_variables_undescribed"
	OutputsUndescribed     = "tf_outputs_undescribed"
	PreventDestroyResource = "tf_prevent_destroy_resources"
	ModuleLines            = "tf_module_lines"
	ParseErrors            = "tf_parse_errors"
	HygieneScore           = "tf_hygiene_score"
)

// Help is the one-line description of each metric.
var Help = map[string]string{
	ReposTotal:             "Repos with at least one Terraform module.",
	ModulesTotal:           "Module directories, by root or child.",
	ResourcesDeclared:      "Resource blocks declared (declarations, not instances).",
	DataSourcesDeclared:    "Data source blocks declared.",
	ModuleCalls:            "Calls to each module address and version.",
	ModuleVersionsBehind:   "Major or minor releases between the pinned version and the latest.",
	ModuleCallsUnpinned:    "Remote module calls with no version or ref.",
	ProviderConstraint:     "Root modules per distinct provider version constraint.",
	RequiredVersion:        "Root modules per Terraform core version constraint; none if absent.",
	Backend:                "Root modules by backend type.",
	VariablesUntyped:       "Variables missing a type.",
	VariablesUndescribed:   "Variables missing a description.",
	OutputsUndescribed:     "Outputs missing a description.",
	PreventDestroyResource: "Resources with prevent_destroy = true.",
	ModuleLines:            "Lines of Terraform configuration.",
	ParseErrors:            "Files that failed to parse.",
	HygieneScore:           "Weighted 0-100 hygiene score.",
}

// Series is one labelled gauge value.
type Series struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels"`
	Value  float64           `json:"value"`
}

// Metrics is the computed catalog.
type Metrics struct {
	GeneratedAt time.Time `json:"generated_at"`
	Repos       int       `json:"repos"`
	Series      []Series  `json:"series"`
}

// Options control aggregation.
type Options struct {
	// PropertyLabels are custom properties promoted to labels.
	PropertyLabels []string
	// DropLabels are removed from every series, summing series that collide.
	DropLabels []string
	// Weights are the hygiene weights by key.
	Weights map[string]float64
	// Versions looks up available module versions; nil skips versions_behind.
	Versions VersionLister
	// Now stamps GeneratedAt.
	Now func() time.Time
	// OnLookupError is called when a version lookup fails; may be nil.
	OnLookupError func(address string, err error)
}

// VersionLister returns the versions available for a module address.
type VersionLister interface {
	Versions(ctx context.Context, kind, address string) ([]string, error)
}

// builder accumulates series keyed by name and label set.
type builder struct {
	index  map[string]int
	series []Series
}

// add adds v to the series with the given name and labels.
func (b *builder) add(name string, labels map[string]string, v float64) {
	key := seriesKey(name, labels)
	if i, ok := b.index[key]; ok {
		b.series[i].Value += v
		return
	}
	b.index[key] = len(b.series)
	b.series = append(b.series, Series{Name: name, Labels: labels, Value: v})
}

// seriesKey returns a stable identity for a name and label set.
func seriesKey(name string, labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	sb.WriteString(name)
	for _, k := range keys {
		sb.WriteString("\x00" + k + "=" + labels[k])
	}
	return sb.String()
}

// with returns a copy of base plus the extra key/value pairs.
func with(base map[string]string, kv ...string) map[string]string {
	out := make(map[string]string, len(base)+len(kv)/2)
	for k, v := range base {
		out[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i]] = kv[i+1]
	}
	return out
}

// Owner returns the owner label for a repo.
func Owner(r facts.ScanResult) string {
	if len(r.Owners) == 0 {
		return "unowned"
	}
	return strings.Join(r.Owners, ",")
}

// orgLabels returns the org and promoted property labels for a repo.
func orgLabels(r facts.ScanResult, props []string) map[string]string {
	l := map[string]string{"org": r.Org}
	for _, p := range props {
		l[p] = r.Properties[p]
	}
	return l
}

// Compute builds the metrics catalog from facts.
func Compute(ctx context.Context, results []facts.ScanResult, opts Options) Metrics {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	b := &builder{index: map[string]int{}}
	m := Metrics{GeneratedAt: opts.Now().UTC().Truncate(time.Second)}
	for _, r := range results {
		if len(r.Modules) == 0 {
			continue
		}
		m.Repos++
		ol := orgLabels(r, opts.PropertyLabels)
		b.add(ReposTotal, ol, 1)
		base := with(ol, "repo", r.Repo, "owner", Owner(r))
		repoMetrics(ctx, b, r, base, opts)
	}
	m.Series = b.series
	if len(opts.DropLabels) > 0 {
		m.Series = Rollup(m.Series, opts.DropLabels)
	}
	sortSeries(m.Series)
	if m.Series == nil {
		m.Series = []Series{}
	}
	return m
}

// repoMetrics adds every per-repo series.
func repoMetrics(ctx context.Context, b *builder, r facts.ScanResult, base map[string]string, opts Options) {
	var untyped, undescVars, undescOut, parseErrs float64
	unpinned := map[string]float64{}
	behind := map[string]int{}
	behindKnown := map[string]bool{}
	for _, mod := range r.Modules {
		b.add(ModulesTotal, with(base, "kind", mod.Kind), 1)
		b.add(ModuleLines, with(base, "kind", mod.Kind), float64(mod.Lines))
		for _, res := range mod.Resources {
			b.add(ResourcesDeclared, with(base, "provider", res.Provider, "resource_type", res.Type), 1)
			if res.PreventDestroy {
				b.add(PreventDestroyResource, with(base, "resource_type", res.Type), 1)
			}
		}
		for _, ds := range mod.DataSources {
			b.add(DataSourcesDeclared, with(base, "provider", ds.Provider), 1)
		}
		for _, c := range mod.ModuleCalls {
			b.add(ModuleCalls, with(base, "address", c.Address, "source_kind", c.SourceKind, "version", c.Version), 1)
			if c.SourceKind == source.KindLocal {
				continue
			}
			if _, ok := unpinned[c.SourceKind]; !ok {
				unpinned[c.SourceKind] = 0
			}
			if c.Pinned == facts.PinnedNone {
				unpinned[c.SourceKind]++
			}
			if n, ok := versionsBehind(ctx, c, opts); ok {
				behindKnown[c.Address] = true
				if n > behind[c.Address] {
					behind[c.Address] = n
				}
			}
		}
		if mod.Kind == facts.KindRoot {
			for _, p := range mod.Providers {
				name := p.Source
				if name == "" {
					name = p.Name
				}
				b.add(ProviderConstraint, with(base, "provider", name, "constraint", orNone(p.Constraint)), 1)
			}
			b.add(RequiredVersion, with(base, "constraint", orNone(mod.RequiredVersion)), 1)
			b.add(Backend, with(base, "type", orNone(mod.Backend)), 1)
		}
		for _, v := range mod.Variables {
			if !v.HasType {
				untyped++
			}
			if !v.HasDescription {
				undescVars++
			}
		}
		for _, o := range mod.Outputs {
			if !o.HasDescription {
				undescOut++
			}
		}
		parseErrs += float64(mod.ParseErrorFiles())
	}
	for kind, n := range unpinned {
		b.add(ModuleCallsUnpinned, with(base, "source_kind", kind), n)
	}
	for addr := range behindKnown {
		b.add(ModuleVersionsBehind, with(base, "address", addr), float64(behind[addr]))
	}
	b.add(VariablesUntyped, base, untyped)
	b.add(VariablesUndescribed, base, undescVars)
	b.add(OutputsUndescribed, base, undescOut)
	b.add(ParseErrors, base, parseErrs)
	b.add(HygieneScore, base, Hygiene(r, opts.Weights).Score)
}

// versionsBehind looks up how far one call lags. ok is false when it cannot
// be known (no lister, unpinned, a SHA ref, or a failed lookup).
func versionsBehind(ctx context.Context, c facts.ModuleCall, opts Options) (int, bool) {
	if opts.Versions == nil || c.Version == "" {
		return 0, false
	}
	switch c.SourceKind {
	case source.KindRegistry, source.KindPrivateRegistry, source.KindGit:
	default:
		return 0, false
	}
	if c.SourceKind == source.KindGit && isSHA(c.Version) {
		// go-version would otherwise parse a hex SHA such as 0123abc... as a
		// bogus version and report a misleading 0.
		return 0, false
	}
	raw, err := opts.Versions.Versions(ctx, c.SourceKind, c.Address)
	if err != nil {
		if opts.OnLookupError != nil {
			opts.OnLookupError(c.Address, err)
		}
		return 0, false
	}
	avail := versions.Parse(raw)
	if len(avail) == 0 {
		return 0, false
	}
	pinned := versions.Selected(c.Version, avail)
	if pinned == nil {
		return 0, false
	}
	return versions.Behind(pinned, avail), true
}

// isSHA reports whether s looks like an abbreviated or full git commit SHA.
func isSHA(s string) bool {
	if len(s) < 7 || len(s) > 40 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}

// orNone returns s, or "none" when s is empty.
func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "none"
	}
	return s
}

// Rollup removes the given labels from every series and sums series whose
// remaining labels collide. Use it to cap cardinality (for example dropping
// resource_type). Hygiene scores are averaged rather than summed.
func Rollup(in []Series, drop []string) []Series {
	dropSet := map[string]bool{}
	for _, d := range drop {
		dropSet[d] = true
	}
	b := &builder{index: map[string]int{}}
	counts := map[string]int{}
	for _, s := range in {
		l := map[string]string{}
		for k, v := range s.Labels {
			if !dropSet[k] {
				l[k] = v
			}
		}
		b.add(s.Name, l, s.Value)
		counts[seriesKey(s.Name, l)]++
	}
	for i, s := range b.series {
		if s.Name == HygieneScore {
			b.series[i].Value = s.Value / float64(counts[seriesKey(s.Name, s.Labels)])
		}
	}
	return b.series
}

// sortSeries orders series by name, then labels, computing each sort key once.
func sortSeries(series []Series) {
	type keyed struct {
		key string
		s   Series
	}
	ks := make([]keyed, len(series))
	for i, s := range series {
		ks[i] = keyed{seriesKey(s.Name, s.Labels), s}
	}
	sort.SliceStable(ks, func(i, j int) bool { return ks[i].key < ks[j].key })
	for i := range ks {
		series[i] = ks[i].s
	}
}
