package sink

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/rossedman/tfmetrics/internal/facts"
	"github.com/rossedman/tfmetrics/internal/metrics"
	"github.com/rossedman/tfmetrics/internal/source"
)

// sarifResults is one repo with something for every rule, and one without
// Terraform.
func sarifResults() []facts.ScanResult {
	return []facts.ScanResult{
		{Repo: "acme/infra", Modules: []facts.ModuleFacts{
			{Path: "live/prod", ModuleCalls: []facts.ModuleCall{
				{Name: "vpc", Source: "../../modules/vpc", SourceKind: source.KindLocal, Address: "modules/vpc", File: "live/prod/main.tf", Line: 1},
				{Name: "eks", Source: "terraform-aws-modules/eks/aws", SourceKind: source.KindRegistry, Address: "registry.terraform.io/terraform-aws-modules/eks/aws",
					Version: "19.21.0", Pinned: facts.PinnedExact, File: "live/prod/main.tf", Line: 5},
				{Name: "iam", Source: "github.com/acme/tf-iam", SourceKind: source.KindGit, Address: "github.com/acme/tf-iam",
					Pinned: facts.PinnedNone, File: "live/prod/main.tf", Line: 12},
				{Name: "net", Source: "git::https://github.com/acme/tf-net.git?ref=main", SourceKind: source.KindGit, Address: "github.com/acme/tf-net",
					Version: "main", Pinned: facts.PinnedRange, File: "live/prod/net.tf", Line: 2},
				{Name: "s3", Source: "terraform-aws-modules/s3-bucket/aws", SourceKind: source.KindRegistry, Address: "registry.terraform.io/terraform-aws-modules/s3-bucket/aws",
					Version: "~> 3.15", Pinned: facts.PinnedRange, File: "live/prod/main.tf", Line: 20},
			}},
			{Path: "old", ModuleCalls: []facts.ModuleCall{
				// Facts from before calls carried a position.
				{Name: "legacy", Source: "github.com/acme/legacy", SourceKind: source.KindGit, Address: "github.com/acme/legacy", Pinned: facts.PinnedNone},
			}, Diagnostics: []facts.Diagnostic{
				{Severity: facts.SeverityError, Summary: "Unclosed configuration block", Detail: "There is no closing brace.", File: "old/main.tf", Line: 7},
				{Severity: facts.SeverityWarning, Summary: "OpenTofu file not parsed", File: "old/x.tofu"},
			}},
		}},
		{Repo: "acme/docs"},
	}
}

// emitSARIF writes the logs and reads acme/infra's back.
func emitSARIF(t *testing.T, s *SARIF, drift []metrics.CallDrift) sarifLog {
	t.Helper()
	if err := s.Emit(context.Background(), sarifResults(), metrics.Metrics{Drift: drift}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(s.Dir, "acme__infra.sarif"))
	if err != nil {
		t.Fatal(err)
	}
	var log sarifLog
	if err := json.Unmarshal(b, &log); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "acme__docs.sarif")); !os.IsNotExist(err) {
		t.Errorf("a repo without Terraform got a log: %v", err)
	}
	return log
}

// summarize lists results as "level rule uri:line".
func summarize(log sarifLog) []string {
	var out []string
	for _, r := range log.Runs[0].Results {
		loc := r.Locations[0].PhysicalLocation
		line := ""
		if loc.Region != nil {
			line = ":" + strconv.Itoa(loc.Region.StartLine)
		}
		out = append(out, r.Level+" "+r.RuleID+" "+loc.ArtifactLocation.URI+line)
	}
	return out
}

func TestSARIF(t *testing.T) {
	t.Parallel()
	drift := []metrics.CallDrift{
		{Repo: "acme/infra", Module: "live/prod", Name: "eks", Behind: 3},
		{Repo: "acme/infra", Module: "live/prod", Name: "s3", Behind: 0},
	}
	log := emitSARIF(t, &SARIF{Dir: t.TempDir(), MinBehind: 1}, drift)
	if log.Version != "2.1.0" || len(log.Runs) != 1 || log.Runs[0].Tool.Driver.Name != "tfmetrics" || len(log.Runs[0].Tool.Driver.Rules) != len(sarifRules) {
		t.Fatalf("log = %+v", log)
	}
	want := []string{
		"note tfmetrics/outdated-module live/prod/main.tf:5",
		"warning tfmetrics/unpinned-module live/prod/main.tf:12",
		"warning tfmetrics/branch-ref live/prod/net.tf:2",
		"warning tfmetrics/unpinned-module old/",
		"error tfmetrics/parse-error old/main.tf:7",
		"warning tfmetrics/parse-warning old/x.tofu",
	}
	got := summarize(log)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("results:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, r := range log.Runs[0].Results {
		rule := log.Runs[0].Tool.Driver.Rules[r.RuleIndex]
		if rule.ID != r.RuleID || rule.DefaultConfiguration.Level != r.Level {
			t.Errorf("result %s points at rule %s", r.RuleID, rule.ID)
		}
		if r.Locations[0].PhysicalLocation.ArtifactLocation.URIBaseID != "%SRCROOT%" {
			t.Errorf("uri base = %+v", r.Locations[0])
		}
	}
	msgs := map[string]string{}
	for _, r := range log.Runs[0].Results {
		msgs[r.RuleID+" "+r.Locations[0].PhysicalLocation.ArtifactLocation.URI] = r.Message.Text
	}
	for key, want := range map[string]string{
		"tfmetrics/outdated-module live/prod/main.tf": `Module "eks" (registry.terraform.io/terraform-aws-modules/eks/aws at 19.21.0) is 3 release lines behind the latest.`,
		"tfmetrics/branch-ref live/prod/net.tf":       `Module "net" follows the branch "main"; pin a release tag or commit SHA.`,
		"tfmetrics/parse-error old/main.tf":           "Unclosed configuration block: There is no closing brace.",
	} {
		if msgs[key] != want {
			t.Errorf("%s message = %q, want %q", key, msgs[key], want)
		}
	}
}

func TestSARIFOutdatedThreshold(t *testing.T) {
	t.Parallel()
	drift := []metrics.CallDrift{{Repo: "acme/infra", Module: "live/prod", Name: "eks", Behind: 1}}
	for _, tt := range []struct {
		min  int
		want bool
	}{{0, false}, {1, true}, {2, false}} {
		log := emitSARIF(t, &SARIF{Dir: t.TempDir(), MinBehind: tt.min}, drift)
		got := strings.Contains(strings.Join(summarize(log), "\n"), "outdated-module")
		if got != tt.want {
			t.Errorf("min_behind %d: outdated reported = %v, want %v", tt.min, got, tt.want)
		}
		if tt.want && !strings.Contains(log.Runs[0].Results[0].Message.Text, "1 release line behind") {
			t.Errorf("singular message: %q", log.Runs[0].Results[0].Message.Text)
		}
	}
}

func TestSARIFErrors(t *testing.T) {
	t.Parallel()
	if err := (&SARIF{Dir: "s3://bucket/x"}).Emit(context.Background(), nil, metrics.Metrics{}); err == nil {
		t.Error("object store path should fail")
	}
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := (&SARIF{Dir: blocker}).Emit(context.Background(), sarifResults(), metrics.Metrics{}); err == nil {
		t.Error("a file where the directory goes should fail")
	}
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "acme__infra.sarif"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := (&SARIF{Dir: dir}).Emit(context.Background(), sarifResults(), metrics.Metrics{}); err == nil {
		t.Error("an unwritable log should fail")
	}
}

func BenchmarkSARIFLog(b *testing.B) {
	r := sarifResults()[0]
	for range 6 {
		r.Modules = append(r.Modules, r.Modules...)
	}
	drift := map[string]int{"acme/infra\x00live/prod\x00eks": 3}
	for b.Loop() {
		_ = sarifFor(r, drift, 1)
	}
}
