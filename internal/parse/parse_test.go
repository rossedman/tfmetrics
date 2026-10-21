package parse

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/rossedman/tfmetrics/internal/facts"
	"github.com/rossedman/tfmetrics/internal/source"
)

func fixtureRoot() string {
	return filepath.Join("..", "..", "testdata", "repos", "acme-infra")
}

func writeTree(t testing.TB, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func callsByName(m facts.ModuleFacts) map[string]facts.ModuleCall {
	out := map[string]facts.ModuleCall{}
	for _, c := range m.ModuleCalls {
		out[c.Name] = c
	}
	return out
}

func assertNonNil(t testing.TB, m facts.ModuleFacts) {
	t.Helper()
	if m.Providers == nil || m.Resources == nil || m.DataSources == nil || m.ModuleCalls == nil ||
		m.Variables == nil || m.Outputs == nil || m.Diagnostics == nil {
		t.Errorf("nil slice in %+v", m)
	}
	if m.Kind != facts.KindRoot && m.Kind != facts.KindChild {
		t.Errorf("Kind = %q", m.Kind)
	}
}

func TestModuleProd(t *testing.T) {
	t.Parallel()
	m := Module(fixtureRoot(), "live/prod", Options{})
	assertNonNil(t, m)

	if m.Path != "live/prod" || m.Kind != facts.KindRoot || m.Backend != "s3" {
		t.Errorf("path/kind/backend = %q %q %q", m.Path, m.Kind, m.Backend)
	}
	if m.Files != 1 || m.Lines != 97 {
		t.Errorf("files/lines = %d/%d", m.Files, m.Lines)
	}
	if m.RequiredVersion != ">= 1.6.0" {
		t.Errorf("RequiredVersion = %q", m.RequiredVersion)
	}
	if len(m.ModuleCalls) != 6 {
		t.Errorf("module calls = %d, want 6", len(m.ModuleCalls))
	}
	if len(m.Resources) != 3 || len(m.DataSources) != 1 {
		t.Errorf("resources/data = %d/%d, want 3/1", len(m.Resources), len(m.DataSources))
	}
	wantLC := facts.LifecycleFacts{PreventDestroy: 2, IgnoreChanges: 1, CreateBeforeDestroy: 1}
	if m.Lifecycle != wantLC {
		t.Errorf("Lifecycle = %+v, want %+v", m.Lifecycle, wantLC)
	}
	if m.MovedBlocks != 1 || m.ImportBlocks != 1 {
		t.Errorf("moved/import = %d/%d", m.MovedBlocks, m.ImportBlocks)
	}
	if len(m.Outputs) != 1 || m.Outputs[0].Name != "vpc_id" || !m.Outputs[0].HasDescription {
		t.Errorf("outputs = %+v", m.Outputs)
	}
	if len(m.Diagnostics) != 0 {
		t.Errorf("diagnostics = %+v", m.Diagnostics)
	}
	wantProv := []facts.ProviderReq{{Name: "aws", Source: "hashicorp/aws", Constraint: "~> 5.0"}}
	if !reflect.DeepEqual(m.Providers, wantProv) {
		t.Errorf("providers = %+v", m.Providers)
	}

	byName := map[string]facts.BlockRef{}
	for _, r := range m.Resources {
		byName[r.Type+"."+r.Name] = r
	}
	if r := byName["aws_s3_bucket.logs"]; !r.PreventDestroy || r.Count || r.Provider != "aws" {
		t.Errorf("logs = %+v", r)
	}
	if r := byName["aws_kms_key.state"]; !r.PreventDestroy {
		t.Errorf("state = %+v", r)
	}
	if r := byName["aws_instance.bastion"]; !r.Count || r.PreventDestroy || r.ForEach {
		t.Errorf("bastion = %+v", r)
	}
	// Resources are sorted by type, then name.
	for i := 1; i < len(m.Resources); i++ {
		a, b := m.Resources[i-1], m.Resources[i]
		if a.Type > b.Type || (a.Type == b.Type && a.Name > b.Name) {
			t.Errorf("resources unsorted: %+v", m.Resources)
		}
	}
	if m.DataSources[0].Type != "aws_ami" || m.DataSources[0].Name != "al2023" {
		t.Errorf("data = %+v", m.DataSources)
	}

	calls := callsByName(m)
	want := map[string]facts.ModuleCall{
		"vpc":       {Name: "vpc", Source: "../../modules/vpc", SourceKind: "local", Address: "modules/vpc", Version: "", Pinned: ""},
		"eks":       {Name: "eks", Source: "terraform-aws-modules/eks/aws", SourceKind: "registry", Address: "registry.terraform.io/terraform-aws-modules/eks/aws", Version: "19.21.0", Pinned: "exact"},
		"s3_bucket": {Name: "s3_bucket", Source: "terraform-aws-modules/s3-bucket/aws", SourceKind: "registry", Address: "registry.terraform.io/terraform-aws-modules/s3-bucket/aws", Version: "~> 3.15", Pinned: "range"},
		"dns":       {Name: "dns", Source: "git::https://github.com/acme/tf-dns.git?ref=v1.4.0", SourceKind: "git", Address: "github.com/acme/tf-dns", Version: "v1.4.0", Pinned: "exact"},
		"iam":       {Name: "iam", Source: "github.com/acme/tf-iam", SourceKind: "git", Address: "github.com/acme/tf-iam", Version: "", Pinned: "none"},
		"queues":    {Name: "queues", Source: "app.terraform.io/acme/sqs/aws", SourceKind: "private_registry", Address: "app.terraform.io/acme/sqs/aws", Version: "2.1.0", Pinned: "exact", ForEach: true},
	}
	for name, w := range want {
		if got := calls[name]; got != w {
			t.Errorf("call %s:\n got %+v\nwant %+v", name, got, w)
		}
	}
	for i := 1; i < len(m.ModuleCalls); i++ {
		if m.ModuleCalls[i-1].Name > m.ModuleCalls[i].Name {
			t.Errorf("module calls unsorted")
		}
	}
}

func TestModuleStaging(t *testing.T) {
	t.Parallel()
	m := Module(fixtureRoot(), "live/staging", Options{})
	assertNonNil(t, m)
	if m.Kind != facts.KindRoot || m.Backend != "cloud" {
		t.Errorf("kind/backend = %q/%q", m.Kind, m.Backend)
	}
	if len(m.ModuleCalls) != 2 || len(m.Resources) != 1 {
		t.Errorf("calls/resources = %d/%d", len(m.ModuleCalls), len(m.Resources))
	}
	// A required_providers entry without a version has an empty constraint.
	if len(m.Providers) != 1 || m.Providers[0].Source != "hashicorp/aws" || m.Providers[0].Constraint != "" {
		t.Errorf("providers = %+v", m.Providers)
	}
	if m.RequiredVersion != "" {
		t.Errorf("RequiredVersion = %q", m.RequiredVersion)
	}
}

func TestModuleVPCChild(t *testing.T) {
	t.Parallel()
	m := Module(fixtureRoot(), "modules/vpc", Options{})
	assertNonNil(t, m)
	if m.Kind != facts.KindChild || m.Backend != "" {
		t.Errorf("kind/backend = %q/%q", m.Kind, m.Backend)
	}
	if len(m.Variables) != 3 || len(m.Outputs) != 2 || len(m.Resources) != 2 {
		t.Errorf("vars/outputs/resources = %d/%d/%d", len(m.Variables), len(m.Outputs), len(m.Resources))
	}
	vars := map[string]facts.VarFacts{}
	for _, v := range m.Variables {
		vars[v.Name] = v
	}
	if v := vars["cidr"]; !v.HasType || !v.HasDescription || v.HasDefault {
		t.Errorf("cidr = %+v", v)
	}
	if v := vars["tags"]; v.HasType || v.HasDescription || !v.HasDefault {
		t.Errorf("tags = %+v", v)
	}
	if v := vars["enable_flow_logs"]; !v.HasType || v.HasDescription || !v.HasDefault {
		t.Errorf("enable_flow_logs = %+v", v)
	}
	for _, r := range m.Resources {
		if r.Name == "private" && (!r.ForEach || r.Count) {
			t.Errorf("subnet = %+v", r)
		}
	}
	outs := map[string]bool{}
	for _, o := range m.Outputs {
		outs[o.Name] = o.HasDescription
	}
	if !outs["id"] || outs["subnet_ids"] {
		t.Errorf("outputs = %v", outs)
	}
	if m.RequiredVersion != ">= 1.5" {
		t.Errorf("RequiredVersion = %q", m.RequiredVersion)
	}
	if len(m.Providers) != 1 || m.Providers[0].Constraint != ">= 5.0, < 6.0" {
		t.Errorf("providers = %+v", m.Providers)
	}
}

func TestModuleLegacyJSON(t *testing.T) {
	t.Parallel()
	m := Module(fixtureRoot(), "modules/legacy", Options{})
	assertNonNil(t, m)
	if m.Kind != facts.KindChild || m.Files != 1 {
		t.Errorf("kind/files = %q/%d", m.Kind, m.Files)
	}
	if len(m.Resources) != 1 || !m.Resources[0].PreventDestroy || m.Resources[0].Type != "aws_sqs_queue" {
		t.Errorf("resources = %+v", m.Resources)
	}
	if m.Lifecycle.PreventDestroy != 1 {
		t.Errorf("Lifecycle = %+v", m.Lifecycle)
	}
	if len(m.Variables) != 1 || !m.Variables[0].HasType || !m.Variables[0].HasDescription {
		t.Errorf("variables = %+v", m.Variables)
	}
	if len(m.Diagnostics) != 0 {
		t.Errorf("diagnostics = %+v", m.Diagnostics)
	}
}

func TestModuleBroken(t *testing.T) {
	t.Parallel()
	m := Module(fixtureRoot(), "broken", Options{})
	assertNonNil(t, m)
	if m.Kind != facts.KindChild {
		t.Errorf("Kind = %q", m.Kind)
	}
	if m.ParseErrorFiles() != 1 {
		t.Errorf("ParseErrorFiles = %d; diags=%+v", m.ParseErrorFiles(), m.Diagnostics)
	}
	var found bool
	for _, d := range m.Diagnostics {
		if d.Severity == facts.SeverityError {
			found = true
			if d.File != "broken/main.tf" {
				t.Errorf("diagnostic file = %q, want repo-relative broken/main.tf", d.File)
			}
			if d.Line == 0 || d.Summary == "" {
				t.Errorf("diagnostic lacks position/summary: %+v", d)
			}
		}
	}
	if !found {
		t.Errorf("no error diagnostic: %+v", m.Diagnostics)
	}
}

func TestModuleMissingDir(t *testing.T) {
	t.Parallel()
	m := Module(t.TempDir(), "nope", Options{})
	assertNonNil(t, m)
	if m.ParseErrorFiles() == 0 {
		t.Errorf("expected an error diagnostic: %+v", m.Diagnostics)
	}
	var found bool
	for _, d := range m.Diagnostics {
		if d.Summary == "cannot read module directory" && d.File == "nope" && d.Severity == facts.SeverityError {
			found = true
		}
	}
	if !found {
		t.Errorf("diagnostics = %+v", m.Diagnostics)
	}
}

func TestModuleRootOverrides(t *testing.T) {
	t.Parallel()
	got := Module(fixtureRoot(), "modules/vpc", Options{RootOverrides: []string{"modules/*"}})
	if got.Kind != facts.KindRoot {
		t.Errorf("Kind = %q, want root via override", got.Kind)
	}
	got = Module(fixtureRoot(), "modules/vpc", Options{RootOverrides: []string{"live/*"}})
	if got.Kind != facts.KindChild {
		t.Errorf("Kind = %q, want child", got.Kind)
	}
}

func TestModuleNormalizerAliases(t *testing.T) {
	t.Parallel()
	opts := Options{Normalizer: source.NewNormalizer("app.terraform.io", map[string]string{
		"github.com/acme/tf-iam": "app.terraform.io/acme/iam/aws",
	})}
	calls := callsByName(Module(fixtureRoot(), "live/prod", opts))
	iam := calls["iam"]
	if iam.SourceKind != "private_registry" || iam.Address != "app.terraform.io/acme/iam/aws" {
		t.Errorf("iam = %+v", iam)
	}
}

func TestModuleRootDirectory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"main.tf": `module "local" {
  source = "./sub"
}
module "sub_of_sub" {
  source = "./sub/../other"
}
`,
	})
	m := Module(root, ".", Options{})
	calls := callsByName(m)
	if got := calls["local"].Address; got != "sub" {
		t.Errorf("local address = %q, want sub", got)
	}
	if got := calls["sub_of_sub"].Address; got != "other" {
		t.Errorf("address = %q, want other", got)
	}
	if m.Files != 1 {
		t.Errorf("files = %d", m.Files)
	}
}

func TestProviderConfigured(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		file string
		body string
		root bool
	}{
		{"alias only", "main.tf", "provider \"aws\" {\n  alias = \"east\"\n}\n", false},
		{"alias and version", "main.tf", "provider \"aws\" {\n  alias   = \"east\"\n  version = \"~> 5\"\n}\n", false},
		{"empty provider", "main.tf", "provider \"aws\" {}\n", false},
		{"region set", "main.tf", "provider \"aws\" {\n  region = \"us-east-1\"\n}\n", true},
		{"alias and region", "main.tf", "provider \"aws\" {\n  alias  = \"east\"\n  region = \"us-east-1\"\n}\n", true},
		{"nested block", "main.tf", "provider \"aws\" {\n  assume_role {\n    role_arn = \"x\"\n  }\n}\n", true},
		{"json alias only", "main.tf.json", `{"provider":{"aws":{"alias":"east"}}}`, false},
		{"json region", "main.tf.json", `{"provider":{"aws":{"region":"us-east-1"}}}`, true},
		{"json nested block", "main.tf.json", `{"provider":{"aws":{"assume_role":{"role_arn":"x"}}}}`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeTree(t, root, map[string]string{tt.file: tt.body})
			m := Module(root, ".", Options{})
			assertNonNil(t, m)
			want := facts.KindChild
			if tt.root {
				want = facts.KindRoot
			}
			if m.Kind != want {
				t.Errorf("Kind = %q, want %q (diags %+v)", m.Kind, want, m.Diagnostics)
			}
			// A provider block always shows up as a provider, configured or not.
			if len(m.Providers) != 1 || m.Providers[0].Name != "aws" {
				t.Errorf("providers = %+v", m.Providers)
			}
		})
	}
}

func TestModuleBackendAndCloudJSON(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"a/main.tf.json": `{"terraform":{"backend":{"gcs":{"bucket":"b"}}}}`,
		"b/main.tf":      "terraform {\n  backend \"remote\" {}\n}\n",
	})
	if m := Module(root, "a", Options{}); m.Kind != facts.KindRoot || m.Backend != "gcs" {
		t.Errorf("a = %q/%q", m.Kind, m.Backend)
	}
	if m := Module(root, "b", Options{}); m.Kind != facts.KindRoot || m.Backend != "remote" {
		t.Errorf("b = %q/%q", m.Kind, m.Backend)
	}
}

func TestModuleTofuWarning(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"main.tf":        "resource \"aws_vpc\" \"a\" {}\n",
		"extra.tofu":     "resource \"aws_vpc\" \"b\" {}\n",
		"more.tofu.json": `{}`,
	})
	m := Module(root, ".", Options{})
	assertNonNil(t, m)
	if m.Files != 3 {
		t.Errorf("files = %d, want .tf + two tofu files counted", m.Files)
	}
	if len(m.Resources) != 1 {
		t.Errorf("tofu resources must not be parsed; got %+v", m.Resources)
	}
	warns := 0
	for _, d := range m.Diagnostics {
		if d.Severity == facts.SeverityWarning && d.Summary == "OpenTofu file not parsed" {
			warns++
			if d.File != "extra.tofu" && d.File != "more.tofu.json" {
				t.Errorf("warning file = %q", d.File)
			}
		}
		if d.Severity == facts.SeverityError {
			t.Errorf("unexpected error diagnostic %+v", d)
		}
	}
	if warns != 2 {
		t.Errorf("warnings = %d, want 2; diags=%+v", warns, m.Diagnostics)
	}
	if m.ParseErrorFiles() != 0 {
		t.Errorf("warnings must not count as parse errors")
	}
}

func TestModuleTofuWarningFileInSubdir(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTree(t, root, map[string]string{"stack/main.tofu": "x = 1\n"})
	m := Module(root, "stack", Options{})
	if len(m.Diagnostics) == 0 || m.Diagnostics[0].File != "stack/main.tofu" {
		t.Errorf("diagnostics = %+v", m.Diagnostics)
	}
}

func TestModuleBlockFlags(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTree(t, root, map[string]string{"main.tf": `
variable "enabled" {
  default = true
}
resource "a_thing" "pd_false" {
  lifecycle {
    prevent_destroy = false
  }
}
resource "a_thing" "pd_expr" {
  lifecycle {
    prevent_destroy = var.enabled
  }
}
resource "a_thing" "pd_true" {
  lifecycle {
    prevent_destroy = true
  }
}
resource "a_thing" "each" {
  for_each = toset(["x"])
}
data "a_source" "counted" {
  count = 3
}
module "m" {
  source   = "./m"
  count    = 2
}
module "n" {
  source   = "./n"
  for_each = toset(["x"])
}
`})
	m := Module(root, ".", Options{})
	if m.Lifecycle.PreventDestroy != 1 {
		t.Errorf("only literal true counts; got %+v", m.Lifecycle)
	}
	byName := map[string]facts.BlockRef{}
	for _, r := range m.Resources {
		byName[r.Name] = r
	}
	if !byName["pd_true"].PreventDestroy || byName["pd_false"].PreventDestroy || byName["pd_expr"].PreventDestroy {
		t.Errorf("prevent_destroy flags wrong: %+v", byName)
	}
	if !byName["each"].ForEach {
		t.Errorf("each = %+v", byName["each"])
	}
	if len(m.DataSources) != 1 || !m.DataSources[0].Count {
		t.Errorf("data = %+v", m.DataSources)
	}
	calls := callsByName(m)
	if !calls["m"].Count || calls["m"].ForEach || !calls["n"].ForEach || calls["n"].Count {
		t.Errorf("calls = %+v", calls)
	}
}

func TestModuleProvidersMerged(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTree(t, root, map[string]string{"main.tf": `
terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
}
provider "google" {}
resource "random_id" "x" {}
data "null_data_source" "y" {}
resource "aws_vpc" "v" {}
`})
	m := Module(root, ".", Options{})
	var names []string
	byName := map[string]facts.ProviderReq{}
	for _, p := range m.Providers {
		names = append(names, p.Name)
		byName[p.Name] = p
	}
	if want := []string{"aws", "google", "null", "random"}; !reflect.DeepEqual(names, want) {
		t.Errorf("providers = %v, want %v", names, want)
	}
	if byName["aws"].Constraint != "~> 5.0" || byName["google"].Constraint != "" || byName["google"].Source != "" {
		t.Errorf("providers = %+v", byName)
	}
}

func TestModuleLineCounts(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"a.tf": "# one\n# two\n",
		"b.tf": "# no trailing newline",
		"c.tf": "",
	})
	m := Module(root, ".", Options{})
	if m.Files != 3 || m.Lines != 3 {
		t.Errorf("files/lines = %d/%d, want 3/3", m.Files, m.Lines)
	}
}

func TestModuleIgnoresNonTerraformFilesAndDirs(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"main.tf":        "# x\n",
		"vars.tfvars":    "a = 1\n",
		"README.md":      "hi\n",
		"sub/nested.tf":  "resource \"a\" \"b\" {}\n",
		"weird.tf/x.txt": "",
	})
	m := Module(root, ".", Options{})
	if m.Files != 1 || len(m.Resources) != 0 {
		t.Errorf("files=%d resources=%d", m.Files, len(m.Resources))
	}
}

func TestModuleDiagnosticsSorted(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"b.tf": "resource \"a\" \"b\" {\n",
		"a.tf": "resource \"a\" \"c\" {\n",
	})
	m := Module(root, ".", Options{})
	for i := 1; i < len(m.Diagnostics); i++ {
		a, b := m.Diagnostics[i-1], m.Diagnostics[i]
		if a.File > b.File || (a.File == b.File && a.Line > b.Line) {
			t.Errorf("diagnostics unsorted: %+v", m.Diagnostics)
		}
	}
	if m.ParseErrorFiles() != 2 {
		t.Errorf("ParseErrorFiles = %d, diags=%+v", m.ParseErrorFiles(), m.Diagnostics)
	}
}

func TestRelFile(t *testing.T) {
	t.Parallel()
	if got := relFile("/repo", "/repo/a/b.tf"); got != "a/b.tf" {
		t.Errorf("inside: %q", got)
	}
	if got := relFile("/repo", "/elsewhere/b.tf"); got != "/elsewhere/b.tf" {
		t.Errorf("outside: %q", got)
	}
}

func TestCountLines(t *testing.T) {
	t.Parallel()
	tests := map[string]int{"": 0, "\n": 1, "a": 1, "a\n": 1, "a\nb": 2, "a\nb\n": 2, "\n\n": 2}
	for in, want := range tests {
		if got := countLines([]byte(in)); got != want {
			t.Errorf("countLines(%q) = %d, want %d", in, got, want)
		}
	}
}

func FuzzModule(f *testing.F) {
	seeds := []string{
		"",
		"resource \"aws_vpc\" \"a\" {\n  count = 2\n  lifecycle {\n    prevent_destroy = true\n  }\n}\n",
		"terraform {\n  backend \"s3\" {}\n  cloud {}\n}\nprovider \"aws\" {\n  alias = \"x\"\n}\n",
		"module \"m\" {\n  source = \"git::https://github.com/a/b.git?ref=v1\"\n  version = \"1.0.0\"\n}\n",
		"moved {\n  from = a.b\n  to = a.c\n}\nimport {\n  to = a.b\n  id = \"x\"\n}\n",
		"resource \"a\" \"b\" {\n",
		"variable \"x\" {\n  type = string\n}\noutput \"y\" {\n  value = 1\n}\n",
		"provider \"aws\" {\n  region = \"x\"\n}\ndata \"aws_ami\" \"a\" {}\n",
		"{\"resource\":{\"a\":{\"b\":{\"lifecycle\":{\"prevent_destroy\":true}}}}}",
		"{\"terraform\":{\"backend\":{\"s3\":{}}},\"provider\":{\"aws\":{\"alias\":\"x\"}}}",
		"\x00\xff\xfe",
		"resource \"\" \"\" {}\nmodule \"\" {}\nprovider \"\" {}\n",
	}
	for _, s := range seeds {
		f.Add(s, uint8(0))
		f.Add(s, uint8(1))
		f.Add(s, uint8(2))
	}
	exts := []string{"main.tf", "main.tf.json", "main.tofu"}
	f.Fuzz(func(t *testing.T, content string, extSel uint8) {
		root := t.TempDir()
		name := exts[int(extSel)%len(exts)]
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		m := Module(root, ".", Options{})
		if m.Kind != facts.KindRoot && m.Kind != facts.KindChild {
			t.Fatalf("Kind = %q", m.Kind)
		}
		if m.Providers == nil || m.Resources == nil || m.DataSources == nil || m.ModuleCalls == nil ||
			m.Variables == nil || m.Outputs == nil || m.Diagnostics == nil {
			t.Fatalf("nil slice: %+v", m)
		}
		if m.Files != 1 {
			t.Fatalf("Files = %d", m.Files)
		}
		if m.Lines < 0 || m.Lines > strings.Count(content, "\n")+1 {
			t.Fatalf("Lines = %d for %q", m.Lines, content)
		}
		if m.Lifecycle.PreventDestroy > len(m.Resources) {
			t.Fatalf("PreventDestroy %d > resources %d", m.Lifecycle.PreventDestroy, len(m.Resources))
		}
		if m.Kind == facts.KindChild && m.Backend != "" {
			t.Fatalf("child with backend %q", m.Backend)
		}
		for _, c := range m.ModuleCalls {
			switch c.Pinned {
			case "", facts.PinnedExact, facts.PinnedRange, facts.PinnedNone:
			default:
				t.Fatalf("pinned = %q", c.Pinned)
			}
			if (c.SourceKind == source.KindLocal) != (c.Pinned == "") {
				t.Fatalf("local/pinned mismatch: %+v", c)
			}
		}
		for _, d := range m.Diagnostics {
			if d.Severity != facts.SeverityError && d.Severity != facts.SeverityWarning {
				t.Fatalf("severity %q", d.Severity)
			}
		}
	})
}

func BenchmarkModuleProd(b *testing.B) {
	root := fixtureRoot()
	b.ReportAllocs()
	for b.Loop() {
		_ = Module(root, "live/prod", Options{})
	}
}
