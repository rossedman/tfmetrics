package source

import (
	"strings"
	"testing"
)

func TestClassify(t *testing.T) {
	t.Parallel()
	n := NewNormalizer("tf.internal.acme.io", nil)
	tests := []struct {
		name string
		src  string
		want Ref
	}{
		{"empty", "", Ref{Kind: KindUnknown}},
		{"whitespace", "   ", Ref{Kind: KindUnknown}},
		{"local sibling", "./modules/vpc", Ref{Kind: KindLocal, Address: "./modules/vpc"}},
		{"local parent", "../../modules/vpc", Ref{Kind: KindLocal, Address: "../../modules/vpc"}},
		{"local trailing slash", "./vpc/", Ref{Kind: KindLocal, Address: "./vpc"}},
		{"local multiple trailing slashes", ".//", Ref{Kind: KindLocal, Address: "."}},
		{"local dot", ".", Ref{Kind: KindLocal, Address: "."}},
		{"local dotdot", "..", Ref{Kind: KindLocal, Address: ".."}},
		{"public registry", "terraform-aws-modules/vpc/aws", Ref{Kind: KindRegistry, Address: "registry.terraform.io/terraform-aws-modules/vpc/aws"}},
		{"public registry uppercase", "Terraform-AWS-Modules/VPC/aws", Ref{Kind: KindRegistry, Address: "registry.terraform.io/terraform-aws-modules/vpc/aws"}},
		{"public registry subdir", "terraform-aws-modules/vpc/aws//modules/vpc-endpoints", Ref{Kind: KindRegistry, Address: "registry.terraform.io/terraform-aws-modules/vpc/aws//modules/vpc-endpoints"}},
		{"explicit public registry", "registry.terraform.io/hashicorp/consul/aws", Ref{Kind: KindRegistry, Address: "registry.terraform.io/hashicorp/consul/aws"}},
		{"opentofu registry", "registry.opentofu.org/acme/x/aws", Ref{Kind: KindRegistry, Address: "registry.opentofu.org/acme/x/aws"}},
		{"tfc private registry", "app.terraform.io/acme/sqs/aws", Ref{Kind: KindPrivateRegistry, Address: "app.terraform.io/acme/sqs/aws"}},
		{"internal registry", "tf.internal.acme.io/platform/vpc/aws", Ref{Kind: KindPrivateRegistry, Address: "tf.internal.acme.io/platform/vpc/aws"}},
		{"private registry port", "reg.acme.io:8443/platform/vpc/aws", Ref{Kind: KindPrivateRegistry, Address: "reg.acme.io:8443/platform/vpc/aws"}},
		{"private registry subdir", "app.terraform.io/acme/sqs/aws//sub", Ref{Kind: KindPrivateRegistry, Address: "app.terraform.io/acme/sqs/aws//sub"}},
		{"git forced https ref", "git::https://github.com/acme/tf-dns.git?ref=v1.4.0", Ref{Kind: KindGit, Address: "github.com/acme/tf-dns", Ref: "v1.4.0"}},
		{"git forced ssh scp", "git::git@github.com:acme/tf-dns.git?ref=main", Ref{Kind: KindGit, Address: "github.com/acme/tf-dns", Ref: "main"}},
		{"git forced ssh url subdir", "git::ssh://git@github.com/acme/mods.git//vpc?ref=abc1234", Ref{Kind: KindGit, Address: "github.com/acme/mods//vpc", Ref: "abc1234"}},
		{"git forced with port", "git::ssh://git@git.acme.io:7999/infra/mods.git", Ref{Kind: KindGit, Address: "git.acme.io/infra/mods"}},
		{"git forced host only", "git::https://git.acme.io", Ref{Kind: KindGit, Address: "git.acme.io"}},
		{"github shorthand", "github.com/acme/tf-iam", Ref{Kind: KindGit, Address: "github.com/acme/tf-iam"}},
		{"github shorthand subdir ref", "github.com/acme/tf-iam//roles?ref=v2.0.0", Ref{Kind: KindGit, Address: "github.com/acme/tf-iam//roles", Ref: "v2.0.0"}},
		{"github shorthand subpath without //", "github.com/acme/tf-iam/roles", Ref{Kind: KindGit, Address: "github.com/acme/tf-iam/roles"}},
		{"bitbucket shorthand", "bitbucket.org/acme/mods", Ref{Kind: KindGit, Address: "bitbucket.org/acme/mods"}},
		{"git scp", "git@github.com:acme/tf-iam.git", Ref{Kind: KindGit, Address: "github.com/acme/tf-iam"}},
		{"git scp ref", "git@gitlab.com:acme/tf-iam.git?ref=v1.0.0", Ref{Kind: KindGit, Address: "gitlab.com/acme/tf-iam", Ref: "v1.0.0"}},
		{"ssh scheme", "ssh://git@github.com/acme/tf-iam.git", Ref{Kind: KindGit, Address: "github.com/acme/tf-iam"}},
		{"https git suffix", "https://gitlab.acme.io/infra/mods.git", Ref{Kind: KindGit, Address: "gitlab.acme.io/infra/mods"}},
		{"https github no suffix", "https://github.com/acme/tf-iam?ref=1.2.3", Ref{Kind: KindGit, Address: "github.com/acme/tf-iam", Ref: "1.2.3"}},
		{"https creds stripped", "https://user:tok@git.acme.io/infra/mods.git", Ref{Kind: KindGit, Address: "git.acme.io/infra/mods"}},
		{"s3 forced", "s3::https://s3-eu-west-1.amazonaws.com/acme-mods/vpc.zip", Ref{Kind: KindS3, Address: "s3-eu-west-1.amazonaws.com/acme-mods/vpc.zip"}},
		{"s3 bucket url", "https://acme-mods.s3.amazonaws.com/vpc.zip?versionId=1", Ref{Kind: KindS3, Address: "acme-mods.s3.amazonaws.com/vpc.zip"}},
		{"gcs forced", "gcs::https://www.googleapis.com/storage/v1/acme-mods/vpc.zip", Ref{Kind: KindGCS, Address: "www.googleapis.com/storage/v1/acme-mods/vpc.zip"}},
		{"gcs googleapis url", "https://www.googleapis.com/storage/v1/acme/vpc.zip", Ref{Kind: KindGCS, Address: "www.googleapis.com/storage/v1/acme/vpc.zip"}},
		{"googleapis non-storage is http", "https://www.googleapis.com/other/x.zip", Ref{Kind: KindHTTP, Address: "www.googleapis.com/other/x.zip"}},
		{"http archive", "https://example.com/mods/vpc-1.2.3.zip", Ref{Kind: KindHTTP, Address: "example.com/mods/vpc-1.2.3.zip"}},
		{"plain http", "http://example.com/vpc.tgz?x=1", Ref{Kind: KindHTTP, Address: "example.com/vpc.tgz"}},
		{"forced http", "http::https://example.com/vpc.zip", Ref{Kind: KindHTTP, Address: "example.com/vpc.zip"}},
		{"forced https", "https::https://example.com/vpc.zip", Ref{Kind: KindHTTP, Address: "example.com/vpc.zip"}},
		{"unknown forced", "hg::https://example.com/vpc", Ref{Kind: KindUnknown, Address: "hg::https://example.com/vpc"}},
		{"forced non-letters is not forced", "a1::b", Ref{Kind: KindUnknown, Address: "a1::b"}},
		{"bad url", "https://exa mple.com/%zz", Ref{Kind: KindHTTP, Address: "https://exa mple.com/%zz"}},
		{"unknown word", "something-odd", Ref{Kind: KindUnknown, Address: "something-odd"}},
		{"unknown path", "vpc/aws", Ref{Kind: KindUnknown, Address: "vpc/aws"}},
		{"leading whitespace", "  ./x ", Ref{Kind: KindLocal, Address: "./x"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := n.Classify(tt.src); got != tt.want {
				t.Errorf("Classify(%q) = %+v, want %+v", tt.src, got, tt.want)
			}
		})
	}
}

func TestClassifyGitHubWithSubpathIsNotRegistry(t *testing.T) {
	t.Parallel()
	// github.com/org/repo/x matches the registry shape but is a git shorthand.
	got := NewNormalizer("", nil).Classify("github.com/acme/tf-iam/aws")
	if got.Kind != KindGit {
		t.Errorf("Kind = %q, want git", got.Kind)
	}
}

func TestAliases(t *testing.T) {
	t.Parallel()
	n := NewNormalizer("tf.acme.io", map[string]string{
		"git::https://github.com/acme/tf-vpc.git": "tf.acme.io/platform/vpc/aws",
		"git@github.com:acme/tf-eks.git":          "app.terraform.io/acme/eks/aws",
		"https://example.com/old.zip":             "tf.acme.io/platform/old/aws",
		"github.com/acme/tf-misc":                 "misc-name",
		"terraform-aws-modules/vpc/aws":           "tf.acme.io/mirror/vpc/aws",
		"./never":                                 "ignored",
	})
	tests := []struct {
		name string
		src  string
		want Ref
	}{
		{"git alias to internal registry", "github.com/acme/tf-vpc?ref=v3.0.0", Ref{Kind: KindPrivateRegistry, Address: "tf.acme.io/platform/vpc/aws", Ref: "v3.0.0"}},
		{"git alias via other form", "https://github.com/acme/tf-vpc.git", Ref{Kind: KindPrivateRegistry, Address: "tf.acme.io/platform/vpc/aws"}},
		{"scp alias to tfc", "git@github.com:acme/tf-eks.git", Ref{Kind: KindPrivateRegistry, Address: "app.terraform.io/acme/eks/aws"}},
		{"http alias to registry", "https://example.com/old.zip", Ref{Kind: KindPrivateRegistry, Address: "tf.acme.io/platform/old/aws"}},
		{"alias to non-registry keeps kind", "github.com/acme/tf-misc", Ref{Kind: KindGit, Address: "misc-name"}},
		{"registry alias keeps kind", "terraform-aws-modules/vpc/aws", Ref{Kind: KindRegistry, Address: "tf.acme.io/mirror/vpc/aws"}},
		{"no alias", "github.com/acme/other", Ref{Kind: KindGit, Address: "github.com/acme/other"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := n.Classify(tt.src); got != tt.want {
				t.Errorf("Classify(%q) = %+v, want %+v", tt.src, got, tt.want)
			}
		})
	}
}

func TestInternalRegistryLowercased(t *testing.T) {
	t.Parallel()
	if got := NewNormalizer("TF.Acme.IO", nil).InternalRegistry; got != "tf.acme.io" {
		t.Errorf("InternalRegistry = %q", got)
	}
}

func TestPinning(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		ref     Ref
		version string
		want    string
	}{
		{"local", Ref{Kind: KindLocal}, "1.0.0", ""},
		{"registry exact", Ref{Kind: KindRegistry}, "1.2.3", "exact"},
		{"registry exact v prefix", Ref{Kind: KindRegistry}, "v1.2.3", "exact"},
		{"registry equals", Ref{Kind: KindRegistry}, "= 1.2.3", "exact"},
		{"registry equals tight", Ref{Kind: KindPrivateRegistry}, "=1.2.3", "exact"},
		{"registry prerelease exact", Ref{Kind: KindRegistry}, "1.2.3-rc.1", "exact"},
		{"registry tilde", Ref{Kind: KindRegistry}, "~> 1.2", "range"},
		{"registry gte", Ref{Kind: KindRegistry}, ">= 1.0", "range"},
		{"registry comma", Ref{Kind: KindRegistry}, ">= 1.0, < 2.0", "range"},
		{"registry comma exact-ish", Ref{Kind: KindRegistry}, "1.2.3, 1.2.4", "range"},
		{"registry partial", Ref{Kind: KindRegistry}, "1.2", "range"},
		{"registry double equals", Ref{Kind: KindRegistry}, "==1.2.3", "range"},
		{"registry none", Ref{Kind: KindRegistry}, "", "none"},
		{"registry blank", Ref{Kind: KindPrivateRegistry}, "   ", "none"},
		{"git no ref", Ref{Kind: KindGit}, "", "none"},
		{"git sha", Ref{Kind: KindGit, Ref: "a1b2c3d"}, "", "exact"},
		{"git full sha", Ref{Kind: KindGit, Ref: "0123456789abcdef0123456789abcdef01234567"}, "", "exact"},
		{"git semver tag", Ref{Kind: KindGit, Ref: "v1.4.0"}, "", "exact"},
		{"git semver no v", Ref{Kind: KindGit, Ref: "1.4.0"}, "", "exact"},
		{"git branch", Ref{Kind: KindGit, Ref: "main"}, "", "range"},
		{"git short hex is branch", Ref{Kind: KindGit, Ref: "abc12"}, "", "range"},
		{"git partial semver is range", Ref{Kind: KindGit, Ref: "v1.4"}, "", "range"},
		{"git ignores version arg", Ref{Kind: KindGit}, "1.0.0", "none"},
		{"http with version in url", Ref{Kind: KindHTTP, Address: "example.com/mods/vpc-1.2.3.zip"}, "", "exact"},
		{"http v segment", Ref{Kind: KindHTTP, Address: "example.com/v1.2.3/vpc.zip"}, "", "exact"},
		{"http underscore version", Ref{Kind: KindHTTP, Address: "example.com/vpc_1.2.3.tgz"}, "", "exact"},
		{"http latest", Ref{Kind: KindHTTP, Address: "example.com/mods/latest.zip"}, "", "none"},
		{"http two-part version", Ref{Kind: KindHTTP, Address: "example.com/vpc-1.2.zip"}, "", "none"},
		{"s3 versioned", Ref{Kind: KindS3, Address: "b.s3.amazonaws.com/vpc-2.0.1.zip"}, "", "exact"},
		{"s3 unversioned", Ref{Kind: KindS3, Address: "b.s3.amazonaws.com/vpc.zip"}, "", "none"},
		{"gcs ref", Ref{Kind: KindGCS, Address: "x/y", Ref: "z"}, "", "exact"},
		{"unknown", Ref{Kind: KindUnknown, Address: "foo"}, "", "none"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := Pinning(tt.ref, tt.version); got != tt.want {
				t.Errorf("Pinning(%+v, %q) = %q, want %q", tt.ref, tt.version, got, tt.want)
			}
		})
	}
}

func TestVersion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		ref     Ref
		version string
		want    string
	}{
		{"registry constraint", Ref{Kind: KindRegistry}, " ~> 1.2 ", "~> 1.2"},
		{"registry empty", Ref{Kind: KindRegistry}, "", ""},
		{"git ref", Ref{Kind: KindGit, Ref: "v1.4.0"}, "", "v1.4.0"},
		{"git ref wins over version", Ref{Kind: KindGit, Ref: "v1.4.0"}, "9.9.9", "v1.4.0"},
		{"git no ref", Ref{Kind: KindGit}, "", ""},
		{"non-git ref without version", Ref{Kind: KindHTTP, Ref: "r"}, "", "r"},
		{"non-git ref with version", Ref{Kind: KindHTTP, Ref: "r"}, "2.0.0", "2.0.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := Version(tt.ref, tt.version); got != tt.want {
				t.Errorf("Version(%+v, %q) = %q, want %q", tt.ref, tt.version, got, tt.want)
			}
		})
	}
}

var kinds = map[string]bool{
	KindLocal: true, KindRegistry: true, KindPrivateRegistry: true, KindGit: true,
	KindHTTP: true, KindS3: true, KindGCS: true, KindUnknown: true,
}

var classifySeeds = []string{
	"", "./modules/vpc", "../../modules/vpc", ".", "..", "terraform-aws-modules/vpc/aws",
	"terraform-aws-modules/vpc/aws//modules/x", "app.terraform.io/acme/sqs/aws",
	"tf.internal.acme.io/platform/vpc/aws", "git::https://github.com/acme/tf-dns.git?ref=v1.4.0",
	"github.com/acme/tf-iam", "git@github.com:acme/tf-iam.git", "ssh://git@github.com/acme/x.git",
	"https://gitlab.acme.io/infra/mods.git", "s3::https://s3.amazonaws.com/b/k.zip",
	"gcs::https://www.googleapis.com/storage/v1/b/k.zip", "https://b.s3.amazonaws.com/x.zip",
	"https://example.com/vpc-1.2.3.zip", "hg::http://x", "weird", "git::", "::x", "http://", "git@:",
}

func FuzzClassify(f *testing.F) {
	for _, s := range classifySeeds {
		f.Add(s)
	}
	n := NewNormalizer("tf.internal.acme.io", map[string]string{"github.com/acme/tf-iam": "tf.internal.acme.io/acme/iam/aws"})
	f.Fuzz(func(t *testing.T, src string) {
		r := n.Classify(src)
		if !kinds[r.Kind] {
			t.Fatalf("Classify(%q).Kind = %q is not a known kind", src, r.Kind)
		}
		trimmed := strings.TrimSpace(src)
		isLocal := strings.HasPrefix(trimmed, "./") || strings.HasPrefix(trimmed, "../") || trimmed == "." || trimmed == ".."
		if isLocal && r.Kind != KindLocal {
			t.Fatalf("local source %q classified as %q", src, r.Kind)
		}
		if !isLocal && r.Kind == KindLocal {
			t.Fatalf("non-local source %q classified as local", src)
		}
		if trimmed != "" && r.Address == "" && (r.Kind == KindRegistry || r.Kind == KindPrivateRegistry || r.Kind == KindUnknown) {
			t.Fatalf("Classify(%q) lost its address: %+v", src, r)
		}
		// Whitespace around a source never changes the result.
		if r2 := n.Classify("  " + src + "\t"); r2 != r {
			t.Fatalf("whitespace changed result for %q: %+v vs %+v", src, r, r2)
		}
		// Local classification is idempotent on its own address.
		if r.Kind == KindLocal && strings.TrimSpace(r.Address) == r.Address {
			if r2 := n.Classify(r.Address); r2 != r {
				t.Fatalf("local not idempotent for %q: %+v vs %+v", src, r, r2)
			}
		}
		// Pinning is always a known level, and "" exactly for local sources.
		p := Pinning(r, "")
		switch {
		case r.Kind == KindLocal && p != "":
			t.Fatalf("local pinning %q", p)
		case r.Kind != KindLocal && p != "none" && p != "range" && p != "exact":
			t.Fatalf("Pinning(%+v) = %q", r, p)
		}
	})
}

var pinningSeeds = []string{"", "1.2.3", "v1.2.3", "= 1.2.3", "~> 1.2", ">= 1.0, < 2.0", "==1.2.3", "main", "abc1234", "1.2.3-rc.1", "  "}

func FuzzPinning(f *testing.F) {
	for _, k := range []string{KindLocal, KindRegistry, KindPrivateRegistry, KindGit, KindHTTP, KindS3, KindGCS, KindUnknown} {
		for _, s := range pinningSeeds {
			f.Add(k, "", s)
			f.Add(k, s, "")
		}
	}
	f.Add(KindHTTP, "example.com/vpc-1.2.3.zip", "")
	f.Fuzz(func(t *testing.T, kind, refOrAddr, version string) {
		r := Ref{Kind: kind, Ref: refOrAddr, Address: refOrAddr}
		got := Pinning(r, version)
		switch got {
		case "none", "range", "exact":
		case "":
			if kind != KindLocal {
				t.Fatalf("empty pinning for non-local kind %q", kind)
			}
		default:
			t.Fatalf("Pinning(%+v, %q) = %q", r, version, got)
		}
		if kind == KindLocal && got != "" {
			t.Fatalf("local pinned %q", got)
		}
		if (kind == KindRegistry || kind == KindPrivateRegistry) && strings.TrimSpace(version) == "" && got != "none" {
			t.Fatalf("unversioned registry call is %q", got)
		}
		if kind == KindGit && refOrAddr == "" && got != "none" {
			t.Fatalf("git without ref is %q", got)
		}
		// A comma-separated registry constraint is never exact.
		if (kind == KindRegistry || kind == KindPrivateRegistry) && strings.Contains(version, ",") && got != "range" {
			t.Fatalf("comma constraint %q is %q", version, got)
		}
		// Version() agrees with git refs.
		if kind == KindGit && Version(r, version) != refOrAddr {
			t.Fatalf("Version(git) = %q, want ref %q", Version(r, version), refOrAddr)
		}
	})
}

func BenchmarkClassify(b *testing.B) {
	n := NewNormalizer("tf.internal.acme.io", nil)
	b.ReportAllocs()
	for b.Loop() {
		for _, s := range classifySeeds {
			_ = n.Classify(s)
		}
	}
}

func BenchmarkClassifyGit(b *testing.B) {
	n := NewNormalizer("", nil)
	b.ReportAllocs()
	for b.Loop() {
		_ = n.Classify("git::https://github.com/acme/tf-dns.git?ref=v1.4.0")
	}
}
