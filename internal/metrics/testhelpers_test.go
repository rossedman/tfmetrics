package metrics

import (
	"context"
	"fmt"
	"strings"

	"github.com/rossedman/tfmetrics/internal/facts"
	"github.com/rossedman/tfmetrics/internal/source"
)

// fakeLister is a VersionLister backed by a map keyed by address.
type fakeLister struct {
	byAddr map[string][]string
	errs   map[string]error
	calls  []string
}

func (f *fakeLister) Versions(_ context.Context, kind, address string) ([]string, error) {
	f.calls = append(f.calls, kind+" "+address)
	if err := f.errs[address]; err != nil {
		return nil, err
	}
	return f.byAddr[address], nil
}

// find returns the first series with the name and all the given labels.
func find(m Metrics, name string, kv ...string) (Series, bool) {
	for _, s := range m.Series {
		if s.Name != name {
			continue
		}
		ok := true
		for i := 0; i+1 < len(kv); i += 2 {
			if s.Labels[kv[i]] != kv[i+1] {
				ok = false
			}
		}
		if ok {
			return s, true
		}
	}
	return Series{}, false
}

// count returns the number of series with the name.
func count(m Metrics, name string) int {
	n := 0
	for _, s := range m.Series {
		if s.Name == name {
			n++
		}
	}
	return n
}

func label(s Series) string {
	return fmt.Sprintf("%s%v=%v", s.Name, s.Labels, s.Value)
}

func labelsString(ss []Series) string {
	var out []string
	for _, s := range ss {
		out = append(out, label(s))
	}
	return strings.Join(out, "\n")
}

// repoA is a rich repo in org acme, with owners and a property.
func repoA() facts.ScanResult {
	return facts.ScanResult{
		Repo: "acme/infra", Org: "acme",
		Owners:     []string{"@acme/platform", "@acme/sre"},
		Properties: map[string]string{"team": "platform"},
		Modules: []facts.ModuleFacts{
			{
				Path: "live/prod", Kind: facts.KindRoot, Lines: 100,
				RequiredVersion: ">= 1.6",
				Backend:         "s3",
				Providers: []facts.ProviderReq{
					{Name: "aws", Source: "hashicorp/aws", Constraint: "~> 5.0"},
					{Name: "random"},
				},
				Resources: []facts.BlockRef{
					{Type: "aws_s3_bucket", Provider: "aws", PreventDestroy: true},
					{Type: "aws_s3_bucket", Provider: "aws"},
					{Type: "aws_kms_key", Provider: "aws", PreventDestroy: true},
				},
				DataSources: []facts.BlockRef{{Type: "aws_ami", Provider: "aws"}},
				ModuleCalls: []facts.ModuleCall{
					{Name: "vpc", Source: "../vpc", SourceKind: source.KindLocal, Address: "../vpc"},
					{Name: "eks", SourceKind: source.KindRegistry, Address: "registry.terraform.io/tam/eks/aws", Version: "19.21.0", Pinned: facts.PinnedExact},
					{Name: "iam", SourceKind: source.KindGit, Address: "github.com/acme/tf-iam", Pinned: facts.PinnedNone},
				},
				Variables: []facts.VarFacts{
					{Name: "a", HasType: true, HasDescription: true},
					{Name: "b"},
				},
				Outputs: []facts.OutputFacts{{Name: "o"}},
			},
			{
				Path: "live/staging", Kind: facts.KindRoot, Lines: 20,
				Diagnostics: []facts.Diagnostic{
					{Severity: facts.SeverityError, File: "a.tf"},
					{Severity: facts.SeverityError, File: "a.tf"},
					{Severity: facts.SeverityWarning, File: "b.tf"},
				},
				ModuleCalls: []facts.ModuleCall{
					{Name: "eks", SourceKind: source.KindRegistry, Address: "registry.terraform.io/tam/eks/aws", Version: "17.24.0", Pinned: facts.PinnedExact},
				},
			},
			{Path: "modules/vpc", Kind: facts.KindChild, Lines: 30},
		},
	}
}

// repoB is a small repo in org beta, with no owners.
func repoB() facts.ScanResult {
	return facts.ScanResult{
		Repo: "beta/app", Org: "beta",
		Modules: []facts.ModuleFacts{{
			Path: ".", Kind: facts.KindRoot, Lines: 10,
			ModuleCalls: []facts.ModuleCall{
				{Name: "x", SourceKind: source.KindRegistry, Address: "registry.terraform.io/tam/eks/aws", Version: "~> 18.0", Pinned: facts.PinnedRange},
			},
		}},
	}
}

// noModules is a repo without Terraform.
func noModules() facts.ScanResult {
	return facts.ScanResult{Repo: "beta/docs", Org: "beta"}
}
