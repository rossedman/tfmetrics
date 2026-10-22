package report

import (
	"bytes"
	"strings"
	"testing"

	"github.com/rossedman/tfmetrics/internal/facts"
	"github.com/rossedman/tfmetrics/internal/usage"
)

const eksAddr = "registry.terraform.io/terraform-aws-modules/eks/aws"

// usesFixture is three calls to eks, one of them to a //subdir.
func usesFixture() []usage.Use {
	return []usage.Use{
		{Repo: "acme/api", Owner: "unowned", Module: ".", Address: eksAddr, SourceKind: "registry", Pinned: facts.PinnedNone},
		{Repo: "acme/web", Owner: "acme/web-team", Module: "live/prod", File: "live/prod/eks.tf", Line: 3, Address: eksAddr,
			SourceKind: "registry", Version: "19.21.0", Pinned: facts.PinnedExact, Behind: 3, BehindKnown: true},
		{Repo: "acme/web", Owner: "acme/web-team", Module: "live/prod", File: "live/prod/eks.tf", Address: eksAddr + "//modules/karpenter",
			SourceKind: "registry", Version: "~> 21.0", Pinned: facts.PinnedRange, BehindKnown: true},
	}
}

func TestUsageGolden(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	if err := Usage(&b, usesFixture(), UsageOptions{Target: "eks/aws", Behind: true}); err != nil {
		t.Fatal(err)
	}
	golden(t, "usage-modules.txt", b.Bytes())

	providers := []usage.Use{
		{Repo: "acme/api", Owner: "unowned", Module: ".", Address: "registry.terraform.io/hashicorp/aws"},
		{Repo: "acme/web", Owner: "acme/web-team", Module: "live/prod", Address: "registry.terraform.io/hashicorp/aws", Version: "~> 5.0", Behind: 1, BehindKnown: true},
	}
	b.Reset()
	if err := Usage(&b, providers, UsageOptions{Target: "aws", Provider: true}); err != nil {
		t.Fatal(err)
	}
	golden(t, "usage-providers.txt", b.Bytes())
}

func TestUsageEmptyAndManyVersions(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	if err := Usage(&b, nil, UsageOptions{Target: "nothing/here"}); err != nil || b.String() != "nothing/here: no uses found\n" {
		t.Errorf("empty = %q, %v", b.String(), err)
	}
	b.Reset()
	if err := PrettyUsage(&b, nil, UsageOptions{Target: "nothing/here"}); err != nil || !strings.Contains(b.String(), "No uses of nothing/here found") {
		t.Errorf("pretty empty = %q, %v", b.String(), err)
	}

	var uses []usage.Use
	for _, v := range []string{"1.0.0", "1.1.0", "1.2.0", "1.3.0", "1.4.0", "1.5.0", "1.6.0", "1.7.0", "1.8.0", "1.9.0"} {
		uses = append(uses, usage.Use{Repo: "acme/x", Address: eksAddr, Version: v})
	}
	b.Reset()
	if err := Usage(&b, uses, UsageOptions{Target: "eks/aws"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "1.7.0 (1), and 2 more\n") {
		t.Errorf("versions not capped:\n%s", b.String())
	}
}

func TestPrettyUsage(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	if err := PrettyUsage(&b, usesFixture(), UsageOptions{Target: "eks/aws", Behind: true, Width: 200}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"eks/aws  3 calls in 2 repos", "versions ", "│ Address", "│ Behind", "live/prod/eks.tf:3", "│ 3 ", "│ - "} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("missing %q in:\n%s", want, b.String())
		}
	}
	// One address: no Address column, and the headline names the address.
	b.Reset()
	if err := PrettyUsage(&b, usesFixture()[:2], UsageOptions{Target: "eks/aws"}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(b.String(), eksAddr+"  2 calls in 2 repos") || strings.Contains(b.String(), "│ Address") || strings.Contains(b.String(), "Behind") {
		t.Errorf("single address:\n%s", b.String())
	}
}

func TestUsageCSV(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	if err := UsageCSV(&b, usesFixture()); err != nil {
		t.Fatal(err)
	}
	want := "address,repo,owner,module,file,line,source_kind,version,pinned,behind\n" +
		eksAddr + ",acme/api,unowned,.,,,registry,,none,\n" +
		eksAddr + ",acme/web,acme/web-team,live/prod,live/prod/eks.tf,3,registry,19.21.0,exact,3\n" +
		eksAddr + "//modules/karpenter,acme/web,acme/web-team,live/prod,live/prod/eks.tf,,registry,~> 21.0,range,0\n"
	if b.String() != want {
		t.Errorf("csv:\n%s\nwant:\n%s", b.String(), want)
	}
}
