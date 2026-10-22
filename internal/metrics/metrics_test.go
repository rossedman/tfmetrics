package metrics

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/rossedman/tfmetrics/internal/facts"
	"github.com/rossedman/tfmetrics/internal/source"
)

var fixedNow = func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 999, time.UTC) }

func TestComputeCatalog(t *testing.T) {
	t.Parallel()
	m := Compute(context.Background(), []facts.ScanResult{repoA(), repoB(), noModules()}, Options{
		PropertyLabels: []string{"team"},
		Now:            fixedNow,
	})
	if m.Repos != 2 {
		t.Errorf("Repos = %d, want 2 (no-module repo excluded)", m.Repos)
	}
	if want := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC); !m.GeneratedAt.Equal(want) {
		t.Errorf("GeneratedAt = %v, want %v", m.GeneratedAt, want)
	}

	a := []string{"repo", "acme/infra", "owner", "@acme/platform,@acme/sre", "org", "acme", "team", "platform"}
	b := []string{"repo", "beta/app", "owner", "unowned", "org", "beta", "team", ""}
	cat := func(kv []string, extra ...string) []string { return append(append([]string{}, kv...), extra...) }

	tests := []struct {
		name   string
		metric string
		labels []string
		want   float64
	}{
		{"repos acme", ReposTotal, []string{"org", "acme", "team", "platform"}, 1},
		{"repos beta empty property", ReposTotal, []string{"org", "beta", "team", ""}, 1},
		{"modules root", ModulesTotal, cat(a, "kind", "root"), 2},
		{"modules child", ModulesTotal, cat(a, "kind", "child"), 1},
		{"modules beta", ModulesTotal, cat(b, "kind", "root"), 1},
		{"lines root", ModuleLines, cat(a, "kind", "root"), 120},
		{"lines child", ModuleLines, cat(a, "kind", "child"), 30},
		{"resources bucket", ResourcesDeclared, cat(a, "provider", "aws", "resource_type", "aws_s3_bucket"), 2},
		{"resources kms", ResourcesDeclared, cat(a, "provider", "aws", "resource_type", "aws_kms_key"), 1},
		{"data sources", DataSourcesDeclared, cat(a, "provider", "aws"), 1},
		{"prevent destroy bucket", PreventDestroyResource, cat(a, "resource_type", "aws_s3_bucket"), 1},
		{"prevent destroy kms", PreventDestroyResource, cat(a, "resource_type", "aws_kms_key"), 1},
		{"calls local", ModuleCalls, cat(a, "address", "../vpc", "source_kind", "local", "version", ""), 1},
		{"calls eks 19", ModuleCalls, cat(a, "address", "registry.terraform.io/tam/eks/aws", "source_kind", "registry", "version", "19.21.0"), 1},
		{"calls eks 17", ModuleCalls, cat(a, "address", "registry.terraform.io/tam/eks/aws", "source_kind", "registry", "version", "17.24.0"), 1},
		{"calls git unversioned", ModuleCalls, cat(a, "address", "github.com/acme/tf-iam", "source_kind", "git", "version", ""), 1},
		{"unpinned git", ModuleCallsUnpinned, cat(a, "source_kind", "git"), 1},
		{"unpinned registry zero", ModuleCallsUnpinned, cat(a, "source_kind", "registry"), 0},
		{"unpinned beta registry zero", ModuleCallsUnpinned, cat(b, "source_kind", "registry"), 0},
		{"provider prefers source", ProviderConstraint, cat(a, "provider", "hashicorp/aws", "constraint", "~> 5.0"), 1},
		{"provider falls back to name, none constraint", ProviderConstraint, cat(a, "provider", "random", "constraint", "none"), 1},
		{"required version", RequiredVersion, cat(a, "constraint", ">= 1.6"), 1},
		{"required version none", RequiredVersion, cat(a, "constraint", "none"), 1},
		{"required version none beta", RequiredVersion, cat(b, "constraint", "none"), 1},
		{"backend s3", Backend, cat(a, "type", "s3"), 1},
		{"backend none", Backend, cat(a, "type", "none"), 1},
		{"backend none beta", Backend, cat(b, "type", "none"), 1},
		{"vars untyped", VariablesUntyped, a, 1},
		{"vars undescribed", VariablesUndescribed, a, 1},
		{"outputs undescribed", OutputsUndescribed, a, 1},
		{"parse errors distinct files", ParseErrors, a, 1},
		{"parse errors beta zero", ParseErrors, b, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, ok := find(m, tt.metric, tt.labels...)
			if !ok {
				t.Fatalf("no %s series with %v", tt.metric, tt.labels)
			}
			if s.Value != tt.want {
				t.Errorf("%s %v = %v, want %v", tt.metric, tt.labels, s.Value, tt.want)
			}
		})
	}

	// Every catalog metric except the versions_behind ones (no lister) is
	// emitted.
	drift := map[string]bool{ModuleVersionsBehind: true, ProviderVersionsBehind: true, TerraformVersionsBehind: true}
	for name := range Help {
		if drift[name] {
			if count(m, name) != 0 {
				t.Errorf("%s emitted without a lister", name)
			}
			continue
		}
		if count(m, name) == 0 {
			t.Errorf("metric %s missing from output", name)
		}
	}
	// Roots only for provider/required/backend: the child module adds none.
	if got := count(m, RequiredVersion); got != 3 {
		t.Errorf("required_version series = %d, want 3 (acme >=1.6, acme none, beta none)", got)
	}
	// Every series carries org, repo, owner except tf_repos_total.
	for _, s := range m.Series {
		if s.Name == ReposTotal {
			if _, ok := s.Labels["repo"]; ok {
				t.Errorf("repos_total has repo label: %v", s.Labels)
			}
			continue
		}
		for _, k := range []string{"repo", "owner", "org", "team"} {
			if _, ok := s.Labels[k]; !ok {
				t.Errorf("%s missing label %q: %v", s.Name, k, s.Labels)
			}
		}
	}
}

func TestComputeEmpty(t *testing.T) {
	t.Parallel()
	for name, in := range map[string][]facts.ScanResult{"nil": nil, "no modules": {noModules()}} {
		m := Compute(context.Background(), in, Options{})
		if m.Repos != 0 || m.Series == nil || len(m.Series) != 0 {
			t.Errorf("%s: got repos=%d series=%v, want 0 and empty non-nil", name, m.Repos, m.Series)
		}
		if m.GeneratedAt.IsZero() {
			t.Errorf("%s: GeneratedAt unset with default Now", name)
		}
	}
}

func TestOwner(t *testing.T) {
	t.Parallel()
	tests := []struct {
		owners []string
		want   string
	}{
		{nil, "unowned"},
		{[]string{}, "unowned"},
		{[]string{"@a"}, "@a"},
		{[]string{"@a", "@b"}, "@a,@b"},
	}
	for _, tt := range tests {
		if got := Owner(facts.ScanResult{Owners: tt.owners}); got != tt.want {
			t.Errorf("Owner(%v) = %q, want %q", tt.owners, got, tt.want)
		}
	}
}

func TestUnpinnedLocalExcludedAndZeroEmitted(t *testing.T) {
	t.Parallel()
	r := facts.ScanResult{Repo: "o/r", Org: "o", Modules: []facts.ModuleFacts{{
		Kind: facts.KindRoot,
		ModuleCalls: []facts.ModuleCall{
			{SourceKind: source.KindLocal, Address: "../a", Pinned: ""},
			{SourceKind: source.KindLocal, Address: "../b", Pinned: facts.PinnedNone},
			{SourceKind: source.KindHTTP, Address: "x.com/m.zip", Pinned: facts.PinnedNone},
			{SourceKind: source.KindHTTP, Address: "x.com/m2.zip", Pinned: facts.PinnedExact},
			{SourceKind: source.KindGit, Address: "github.com/a/b", Pinned: facts.PinnedExact},
		},
	}}}
	m := Compute(context.Background(), []facts.ScanResult{r}, Options{})
	if _, ok := find(m, ModuleCallsUnpinned, "source_kind", "local"); ok {
		t.Error("local kind appears in unpinned")
	}
	if s, ok := find(m, ModuleCallsUnpinned, "source_kind", "http"); !ok || s.Value != 1 {
		t.Errorf("http unpinned = %v (found %v), want 1", s.Value, ok)
	}
	if s, ok := find(m, ModuleCallsUnpinned, "source_kind", "git"); !ok || s.Value != 0 {
		t.Errorf("git unpinned = %v (found %v), want explicit 0", s.Value, ok)
	}
	if got := count(m, ModuleCallsUnpinned); got != 2 {
		t.Errorf("unpinned series = %d, want 2", got)
	}
}

func TestVersionsBehind(t *testing.T) {
	t.Parallel()
	const eks = "registry.terraform.io/tam/eks/aws"
	call := func(kind, addr, ver string) facts.ModuleCall {
		return facts.ModuleCall{SourceKind: kind, Address: addr, Version: ver, Pinned: facts.PinnedExact}
	}
	r := facts.ScanResult{Repo: "o/r", Org: "o", Modules: []facts.ModuleFacts{
		{Kind: facts.KindRoot, ModuleCalls: []facts.ModuleCall{
			call(source.KindRegistry, eks, "19.1.0"),
			call(source.KindLocal, "../x", "1.0.0"),
			call(source.KindHTTP, "x.com/m.zip", "1.0.0"),
			call(source.KindRegistry, "registry.terraform.io/a/nover/aws", ""),
			call(source.KindGit, "github.com/acme/sha", "0123456789abcdef0123456789abcdef01234567"),
			call(source.KindGit, "github.com/acme/branch", "main"),
			call(source.KindGit, "github.com/acme/tags", "v1.0.0"),
			call(source.KindGit, "github.com/acme/tagpath", "tags/0.4.0"),
			call(source.KindGit, "github.com/acme/mono//vpc", "vpc/v1.0.0"),
			call(source.KindGit, "github.com/acme/mono2//db", "db-1.0.0"),
			call(source.KindPrivateRegistry, "app.terraform.io/acme/vpc/aws", "~> 1.0"),
			call(source.KindRegistry, "registry.terraform.io/a/fail/aws", "1.0.0"),
			call(source.KindRegistry, "registry.terraform.io/a/empty/aws", "1.0.0"),
			call(source.KindRegistry, "registry.terraform.io/a/nomatch/aws", ">= 9.0"),
		}},
		{Kind: facts.KindChild, ModuleCalls: []facts.ModuleCall{
			call(source.KindRegistry, eks, "17.24.0"), // further behind in another module
			call(source.KindRegistry, eks, "20.0.0"),
		}},
	}}
	lister := &fakeLister{
		byAddr: map[string][]string{
			eks:                                   {"17.24.0", "18.0.0", "19.1.0", "19.2.0", "20.0.0", "junk", "21.0.0-beta1"},
			"github.com/acme/sha":                 {"1.0.0", "2.0.0"},
			"github.com/acme/branch":              {"1.0.0", "2.0.0"},
			"github.com/acme/tags":                {"v1.0.0", "v1.1.0", "v1.2.0", "not-a-version"},
			"github.com/acme/tagpath":             {"0.4.0", "0.5.0"},
			"github.com/acme/mono//vpc":           {"vpc/v1.0.0", "vpc/v1.1.0", "db/v1.0.0", "db/v2.0.0", "db/v3.0.0", "v9.0.0"},
			"github.com/acme/mono2//db":           {"db-1.0.0", "db-1.1.0", "db-1.2.0", "vpc-4.0.0"},
			"app.terraform.io/acme/vpc/aws":       {"1.0.0", "1.4.0", "2.0.0"},
			"registry.terraform.io/a/empty/aws":   {"junk", "also-junk"},
			"registry.terraform.io/a/nomatch/aws": {"1.0.0"},
			"registry.terraform.io/a/nover/aws":   {"1.0.0"},
		},
		errs: map[string]error{"registry.terraform.io/a/fail/aws": errors.New("boom")},
	}
	type lookupErr struct {
		addr string
		err  string
	}
	var errs []lookupErr
	m := Compute(context.Background(), []facts.ScanResult{r}, Options{
		Versions:      lister,
		OnLookupError: func(a string, err error) { errs = append(errs, lookupErr{a, err.Error()}) },
	})

	// 17.24.0 trails the 18.0, 19.1, 19.2 and 20.0 lines (4); 19.1.0 only 19.2
	// and 20.0 (2); the max across calls wins and the prerelease 21.0.0-beta1
	// is ignored.
	want := map[string]float64{
		eks:                             4,
		"github.com/acme/tags":          2, // v1.0.0 -> 1.1, 1.2
		"github.com/acme/tagpath":       1, // tags/0.4.0 -> 0.5
		"github.com/acme/mono//vpc":     1, // only vpc/ tags count: 1.1
		"github.com/acme/mono2//db":     2, // only db- tags count: 1.1, 1.2
		"app.terraform.io/acme/vpc/aws": 1, // ~> 1.0 selects 1.4.0 -> 2.0
	}
	var got []string
	for _, s := range m.Series {
		if s.Name != ModuleVersionsBehind {
			continue
		}
		got = append(got, s.Labels["address"])
		if w, ok := want[s.Labels["address"]]; !ok {
			t.Errorf("unexpected versions_behind for %s = %v", s.Labels["address"], s.Value)
		} else if s.Value != w {
			t.Errorf("versions_behind %s = %v, want %v", s.Labels["address"], s.Value, w)
		}
	}
	sort.Strings(got)
	if len(got) != len(want) {
		t.Errorf("versions_behind addresses = %v, want %d of them", got, len(want))
	}
	if !reflect.DeepEqual(errs, []lookupErr{{"registry.terraform.io/a/fail/aws", "boom"}}) {
		t.Errorf("lookup errors = %v", errs)
	}
	for _, c := range lister.calls {
		if c == "local ../x" || c == "http x.com/m.zip" || c == "registry registry.terraform.io/a/nover/aws" {
			t.Errorf("unexpected lister call %q", c)
		}
	}
}

func TestVersionsBehindNoLister(t *testing.T) {
	t.Parallel()
	m := Compute(context.Background(), []facts.ScanResult{repoA()}, Options{OnLookupError: func(string, error) { t.Error("called") }})
	if count(m, ModuleVersionsBehind) != 0 {
		t.Error("versions_behind emitted with nil lister")
	}
}

func TestRollup(t *testing.T) {
	t.Parallel()
	in := []Series{
		{Name: ResourcesDeclared, Labels: map[string]string{"repo": "a", "resource_type": "x"}, Value: 2},
		{Name: ResourcesDeclared, Labels: map[string]string{"repo": "a", "resource_type": "y"}, Value: 3},
		{Name: ResourcesDeclared, Labels: map[string]string{"repo": "b", "resource_type": "x"}, Value: 7},
		{Name: HygieneScore, Labels: map[string]string{"repo": "a", "resource_type": "x"}, Value: 100},
		{Name: HygieneScore, Labels: map[string]string{"repo": "a", "resource_type": "y"}, Value: 50},
		{Name: HygieneScore, Labels: map[string]string{"repo": "b"}, Value: 40},
	}
	got := Rollup(in, []string{"resource_type"})
	if len(got) != 4 {
		t.Fatalf("Rollup returned %d series: %s", len(got), labelsString(got))
	}
	if s, _ := find(Metrics{Series: got}, ResourcesDeclared, "repo", "a"); s.Value != 5 {
		t.Errorf("resources a = %v, want 5 (summed)", s.Value)
	}
	if s, _ := find(Metrics{Series: got}, ResourcesDeclared, "repo", "b"); s.Value != 7 {
		t.Errorf("resources b = %v, want 7", s.Value)
	}
	if s, _ := find(Metrics{Series: got}, HygieneScore, "repo", "a"); s.Value != 75 {
		t.Errorf("hygiene a = %v, want 75 (averaged)", s.Value)
	}
	if s, _ := find(Metrics{Series: got}, HygieneScore, "repo", "b"); s.Value != 40 {
		t.Errorf("hygiene b = %v, want 40", s.Value)
	}
	for _, s := range got {
		if _, ok := s.Labels["resource_type"]; ok {
			t.Errorf("resource_type not dropped: %v", s.Labels)
		}
	}
	if in[0].Labels["resource_type"] != "x" {
		t.Error("Rollup mutated its input")
	}
}

func TestComputeDropLabels(t *testing.T) {
	t.Parallel()
	m := Compute(context.Background(), []facts.ScanResult{repoA()}, Options{DropLabels: []string{"resource_type", "provider"}})
	s, ok := find(m, ResourcesDeclared, "repo", "acme/infra")
	if !ok || s.Value != 3 {
		t.Errorf("resources after drop = %v (found %v), want 3 summed", s.Value, ok)
	}
	if got := count(m, ResourcesDeclared); got != 1 {
		t.Errorf("resources series = %d, want 1", got)
	}
	if len(find2(m, HygieneScore)) != 1 {
		t.Error("hygiene series count changed")
	}
}

func find2(m Metrics, name string) []Series {
	var out []Series
	for _, s := range m.Series {
		if s.Name == name {
			out = append(out, s)
		}
	}
	return out
}

func TestComputeDeterministic(t *testing.T) {
	t.Parallel()
	opts := Options{PropertyLabels: []string{"team"}, Now: fixedNow}
	in := []facts.ScanResult{repoA(), repoB(), noModules()}
	first := Compute(context.Background(), in, opts)
	for range 20 {
		again := Compute(context.Background(), in, opts)
		if !reflect.DeepEqual(first, again) {
			t.Fatal("Compute output differs between runs")
		}
	}
	// Input order does not matter either.
	rev := Compute(context.Background(), []facts.ScanResult{repoB(), noModules(), repoA()}, opts)
	if !reflect.DeepEqual(first, rev) {
		t.Error("Compute output depends on input order")
	}
	if !sort.SliceIsSorted(first.Series, func(i, j int) bool {
		if first.Series[i].Name != first.Series[j].Name {
			return first.Series[i].Name < first.Series[j].Name
		}
		return seriesKey("", first.Series[i].Labels) < seriesKey("", first.Series[j].Labels)
	}) {
		t.Error("series not sorted by name then labels")
	}
}

func TestWithOddKV(t *testing.T) {
	t.Parallel()
	got := with(map[string]string{"a": "1"}, "b", "2", "dangling")
	if !reflect.DeepEqual(got, map[string]string{"a": "1", "b": "2"}) {
		t.Errorf("with = %v", got)
	}
}

func TestOrNone(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{"": "none", "  ": "none", "x": "x"} {
		if got := orNone(in); got != want {
			t.Errorf("orNone(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestProviderAndTerraformVersionsBehind(t *testing.T) {
	t.Parallel()
	r := facts.ScanResult{Repo: "o/r", Org: "o", Modules: []facts.ModuleFacts{
		{Kind: facts.KindRoot, RequiredVersion: "~> 1.5.0", Providers: []facts.ProviderReq{
			{Name: "aws", Source: "hashicorp/aws", Constraint: "~> 4.0"},
			{Name: "random", Constraint: "~> 3.1"}, // no source: the implied hashicorp/random
			{Name: "google", Source: "registry.terraform.io/hashicorp/google", Constraint: ">= 4.0"},
			{Name: "null", Source: "hashicorp/null"},                      // no constraint: no lookup
			{Name: "terraform", Source: "terraform.io/builtin/terraform"}, // built in: no lookup
			{Name: "broken", Source: "acme/broken", Constraint: "~> 1.0"}, // lookup fails
		}},
		{Kind: facts.KindRoot, RequiredVersion: "~> 1.9"},
		{Kind: facts.KindChild, RequiredVersion: "~> 0.12.0", Providers: []facts.ProviderReq{
			{Name: "aws", Source: "hashicorp/aws", Constraint: ">= 3.0"}, // selects the latest
		}},
	}}
	lister := &fakeLister{
		byAddr: map[string][]string{
			"registry.terraform.io/hashicorp/aws":    {"3.76.1", "4.0.0", "4.67.0", "5.0.0", "5.10.0", "6.0.0"},
			"registry.terraform.io/hashicorp/random": {"3.1.0", "3.6.0"},
			"registry.terraform.io/hashicorp/google": {"4.0.0", "6.1.0"},
			TerraformReleases:                        {"v0.12.31", "v1.5.0", "v1.5.7", "v1.6.0", "v1.9.0", "v1.10.0", "v1.11.0-beta1"},
		},
		errs: map[string]error{"registry.terraform.io/acme/broken": errors.New("boom")},
	}
	var failed []string
	m := Compute(context.Background(), []facts.ScanResult{r}, Options{
		Versions:      lister,
		OnLookupError: func(addr string, _ error) { failed = append(failed, addr) },
	})
	// ~> 4.0 selects 4.67.0, behind the 5.0, 5.10 and 6.0 lines; the child's
	// >= 3.0 selects 6.0.0 (0), and the larger value wins.
	for provider, want := range map[string]float64{"hashicorp/aws": 3, "hashicorp/random": 0, "hashicorp/google": 0} {
		s, ok := find(m, ProviderVersionsBehind, "provider", provider)
		if !ok || s.Value != want {
			t.Errorf("%s: %v, %v; want %v", provider, s.Value, ok, want)
		}
	}
	if got := count(m, ProviderVersionsBehind); got != 3 {
		t.Errorf("provider series = %d, want 3", got)
	}
	// Roots only: ~> 1.5.0 selects 1.5.7, behind 1.6, 1.9 and 1.10; the
	// child's ~> 0.12.0 does not count.
	if s, ok := find(m, TerraformVersionsBehind, "repo", "o/r"); !ok || s.Value != 3 {
		t.Errorf("terraform behind = %v, %v; want 3", s.Value, ok)
	}
	if !slices.Equal(failed, []string{"registry.terraform.io/acme/broken"}) {
		t.Errorf("lookup errors for %v", failed)
	}
	for _, c := range lister.calls {
		if strings.Contains(c, "null") || strings.Contains(c, "builtin") {
			t.Errorf("unexpected lookup %q", c)
		}
	}

	// Prefetch asks for the same lookups, once each.
	pre := &fakeLister{byAddr: lister.byAddr, errs: lister.errs}
	Prefetch(context.Background(), []facts.ScanResult{r}, pre, 1, nil)
	want := []string{
		"provider registry.terraform.io/hashicorp/aws", "provider registry.terraform.io/hashicorp/random",
		"provider registry.terraform.io/hashicorp/google", "provider registry.terraform.io/acme/broken",
		"git " + TerraformReleases,
	}
	slices.Sort(want)
	got := slices.Clone(pre.calls)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("prefetched %v, want %v", got, want)
	}
}
