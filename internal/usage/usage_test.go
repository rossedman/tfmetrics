package usage

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/rossedman/tfmetrics/internal/facts"
	"github.com/rossedman/tfmetrics/internal/source"
)

const eks = "registry.terraform.io/terraform-aws-modules/eks/aws"

// fixture is two repos calling eks and requiring aws in different ways.
func fixture() []facts.ScanResult {
	return []facts.ScanResult{
		{Repo: "acme/web", Owners: []string{"acme/web-team"}, Modules: []facts.ModuleFacts{
			{Path: "live/prod", ModuleCalls: []facts.ModuleCall{
				{Name: "eks", Address: eks, SourceKind: source.KindRegistry, Version: "19.21.0", Pinned: facts.PinnedExact, File: "live/prod/eks.tf", Line: 3},
				{Name: "karpenter", Address: eks + "//modules/karpenter", SourceKind: source.KindRegistry, Version: "~> 21.0", Pinned: facts.PinnedRange, File: "live/prod/eks.tf", Line: 20},
				{Name: "vpc", Address: "registry.terraform.io/terraform-aws-modules/vpc/aws", SourceKind: source.KindRegistry, Version: "5.0.0"},
			}, Providers: []facts.ProviderReq{{Name: "aws", Source: "hashicorp/aws", Constraint: "~> 5.0"}}},
		}},
		{Repo: "acme/api", Modules: []facts.ModuleFacts{
			{Path: ".", ModuleCalls: []facts.ModuleCall{
				{Name: "cluster", Address: eks, SourceKind: source.KindRegistry, Pinned: facts.PinnedNone},
				{Name: "eks2", Address: "registry.terraform.io/acme/notreally-eks/aws", SourceKind: source.KindRegistry, Version: "1.0.0"},
			}, Providers: []facts.ProviderReq{{Name: "aws"}, {Name: "google", Source: "hashicorp/google", Constraint: ">= 4"}}},
		}},
	}
}

func TestMatch(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		address, target string
		want            bool
	}{
		{eks, eks, true},
		{eks, "terraform-aws-modules/eks/aws", true},
		{eks, "eks/aws", true},
		{eks, "EKS/AWS/", true},
		{eks + "//modules/karpenter", "terraform-aws-modules/eks/aws", true},
		{eks + "//modules/karpenter", eks + "//modules/karpenter", true},
		{"registry.terraform.io/acme/notreally-eks/aws", "eks/aws", false}, // whole segments only
		{eks, "aws/eks", false},
		{eks, "", false},
	} {
		if got := Match(tt.address, tt.target); got != tt.want {
			t.Errorf("Match(%q, %q) = %v, want %v", tt.address, tt.target, got, tt.want)
		}
	}
}

func TestFindModules(t *testing.T) {
	t.Parallel()
	uses := Find(fixture(), Query{Target: "eks/aws"})
	var got []string
	for _, u := range uses {
		got = append(got, u.Repo+" "+u.Module+" "+u.Address+" "+u.Version)
	}
	want := []string{
		"acme/api . " + eks + " ",
		"acme/web live/prod " + eks + " 19.21.0",
		"acme/web live/prod " + eks + "//modules/karpenter ~> 21.0",
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %q\nwant %q", got, want)
	}
	if u := uses[1]; u.Owner != "acme/web-team" || u.File != "live/prod/eks.tf" || u.Line != 3 || u.SourceKind != source.KindRegistry {
		t.Errorf("use = %+v", u)
	}
	if uses[0].Owner != "unowned" {
		t.Errorf("owner = %q", uses[0].Owner)
	}
}

func TestFindProviders(t *testing.T) {
	t.Parallel()
	uses := Find(fixture(), Query{Target: "hashicorp/aws", Provider: true})
	if len(uses) != 2 || uses[0].Repo != "acme/api" || uses[0].Version != "" || uses[1].Version != "~> 5.0" ||
		uses[0].Address != "registry.terraform.io/hashicorp/aws" {
		t.Errorf("uses = %+v", uses)
	}
}

type fakeLister map[string][]string

// Versions answers like the registry resolver: per module, ignoring //subdir.
func (f fakeLister) Versions(_ context.Context, _, addr string) ([]string, error) {
	addr, _, _ = strings.Cut(addr, "//")
	if v, ok := f[addr]; ok {
		return v, nil
	}
	return nil, errors.New("not found")
}

func TestBehindAndFilter(t *testing.T) {
	t.Parallel()
	lister := fakeLister{
		eks:                                   {"19.21.0", "20.0.0", "21.0.0", "21.3.0"},
		"registry.terraform.io/hashicorp/aws": {"5.0.0", "5.9.0", "6.0.0"},
	}
	uses := Find(fixture(), Query{Target: "eks/aws"})
	var failed []string
	Behind(context.Background(), uses, false, lister, func(a string, _ error) { failed = append(failed, a) })
	// 19.21.0 trails 20.0, 21.0 and 21.3; ~> 21.0 selects 21.3.0; the
	// unpinned call cannot be known.
	if uses[0].BehindKnown || !uses[1].BehindKnown || uses[1].Behind != 3 || !uses[2].BehindKnown || uses[2].Behind != 0 {
		t.Errorf("behind = %+v", uses)
	}
	if len(failed) != 0 {
		t.Errorf("lookup errors: %v", failed)
	}
	if got := AtLeastBehind(uses, 1); len(got) != 1 || got[0].Version != "19.21.0" {
		t.Errorf("at least 1 behind = %+v", got)
	}

	providers := Find(fixture(), Query{Target: "aws", Provider: true})
	Behind(context.Background(), providers, true, lister, nil)
	// No constraint: unknown. ~> 5.0 selects 5.9.0, behind the 6.0 line.
	if providers[0].BehindKnown || !providers[1].BehindKnown || providers[1].Behind != 1 {
		t.Errorf("provider behind = %+v", providers)
	}
}

func TestSummarize(t *testing.T) {
	t.Parallel()
	uses := Find(fixture(), Query{Target: "eks/aws"})
	s := Summarize(uses)
	if s.Uses != 3 || s.Repos != 2 || len(s.Addresses) != 2 {
		t.Errorf("summary = %+v", s)
	}
	want := []VersionCount{{"", 1}, {"19.21.0", 1}, {"~> 21.0", 1}}
	if !slices.Equal(s.Versions, want) {
		t.Errorf("versions = %+v", s.Versions)
	}
	if s := Summarize(nil); s.Uses != 0 || s.Repos != 0 {
		t.Errorf("empty = %+v", s)
	}
}

func FuzzMatch(f *testing.F) {
	for _, s := range [][2]string{{eks, "eks/aws"}, {eks + "//modules/x", eks}, {"a/b", "/"}, {"", ""}} {
		f.Add(s[0], s[1])
	}
	f.Fuzz(func(t *testing.T, address, target string) {
		got := Match(address, target)
		// Matching ignores case and surrounding slashes in the target.
		upper, upperTarget := strings.ToUpper(address), strings.ToUpper(strings.TrimSpace(target))
		foldsBack := strings.EqualFold(upper, address) && strings.EqualFold(upperTarget, strings.TrimSpace(target))
		if foldsBack && Match(upper, "/"+upperTarget+"/") != got {
			t.Fatalf("case or slashes changed Match(%q, %q)", address, target)
		}
		// An address always matches itself, when it is not just slashes.
		if strings.Trim(strings.TrimSpace(address), "/") != "" && address == strings.TrimSpace(address) && !strings.HasPrefix(address, "/") && !strings.HasSuffix(address, "/") && !Match(address, address) {
			t.Fatalf("%q does not match itself", address)
		}
	})
}

func BenchmarkFind(b *testing.B) {
	results := fixture()
	for range 6 {
		results = append(results, results...)
	}
	for b.Loop() {
		_ = Find(results, Query{Target: "eks/aws"})
		_ = Find(results, Query{Target: "hashicorp/aws", Provider: true})
	}
}
