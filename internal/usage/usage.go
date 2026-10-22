// Package usage answers "who uses this?" across facts files: every call to a
// module, or every requirement of a provider, with where it is, the version
// it asks for, how far that version is behind, and who owns the repo.
package usage

import (
	"context"
	"sort"
	"strings"

	"github.com/rossedman/tfmetrics/internal/facts"
	"github.com/rossedman/tfmetrics/internal/metrics"
	"github.com/rossedman/tfmetrics/internal/source"
)

// Use is one call to a module, or one requirement of a provider.
type Use struct {
	Repo       string `json:"repo"`
	Owner      string `json:"owner"`
	Module     string `json:"module"`         // the calling module's directory
	File       string `json:"file,omitempty"` // the module block's file; empty for providers and older facts
	Line       int    `json:"line,omitempty"`
	Address    string `json:"address"`               // the module address, or the provider's host/namespace/type
	SourceKind string `json:"source_kind,omitempty"` // for module calls: registry, git and so on
	Version    string `json:"version"`               // the constraint or ref as written; "" when unpinned
	Pinned     string `json:"pinned,omitempty"`
	// Behind counts release lines between the selected version and the
	// latest; it is set only when BehindKnown.
	Behind      int  `json:"behind"`
	BehindKnown bool `json:"behind_known"`
}

// Query selects what to look for.
type Query struct {
	// Target is a module address or provider source, whole or as its trailing
	// path segments: "terraform-aws-modules/eks/aws" and "eks/aws" both match
	// registry.terraform.io/terraform-aws-modules/eks/aws, and match its
	// //subdir calls too. Matching ignores case.
	Target string
	// Provider searches provider requirements instead of module calls.
	Provider bool
}

// Find returns every use matching q, sorted by address, repo, module and
// line.
func Find(results []facts.ScanResult, q Query) []Use {
	var out []Use
	for _, r := range results {
		owner := metrics.Owner(r)
		for _, m := range r.Modules {
			if q.Provider {
				for _, p := range m.Providers {
					addr, ok := source.ProviderAddress(p.Source, p.Name)
					if ok && Match(addr, q.Target) {
						out = append(out, Use{Repo: r.Repo, Owner: owner, Module: m.Path, Address: addr, Version: p.Constraint})
					}
				}
				continue
			}
			for _, c := range m.ModuleCalls {
				if Match(c.Address, q.Target) {
					out = append(out, Use{
						Repo: r.Repo, Owner: owner, Module: m.Path, File: c.File, Line: c.Line,
						Address: c.Address, SourceKind: c.SourceKind, Version: c.Version, Pinned: c.Pinned,
					})
				}
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Address != b.Address {
			return a.Address < b.Address
		}
		if a.Repo != b.Repo {
			return a.Repo < b.Repo
		}
		if a.Module != b.Module {
			return a.Module < b.Module
		}
		return a.Line < b.Line
	})
	return out
}

// Match reports whether address is target, or ends with target as whole path
// segments. An address with a //subdir also matches through its module part.
func Match(address, target string) bool {
	address = strings.ToLower(address)
	target = strings.ToLower(strings.Trim(strings.TrimSpace(target), "/"))
	if target == "" {
		return false
	}
	module, _, _ := strings.Cut(address, "//")
	for _, a := range []string{address, module} {
		if a == target || strings.HasSuffix(a, "/"+target) {
			return true
		}
	}
	return false
}

// Behind fills in Behind for each use whose drift can be known, looking up
// versions through lister. onErr, when set, hears about failed lookups.
func Behind(ctx context.Context, uses []Use, provider bool, lister metrics.VersionLister, onErr func(string, error)) {
	for i := range uses {
		u := &uses[i]
		if provider {
			if u.Version != "" {
				u.Behind, u.BehindKnown = metrics.ProviderBehind(ctx, u.Address, u.Version, lister, onErr)
			}
			continue
		}
		call := facts.ModuleCall{Address: u.Address, SourceKind: u.SourceKind, Version: u.Version, Pinned: u.Pinned}
		u.Behind, u.BehindKnown = metrics.CallBehind(ctx, call, lister, onErr)
	}
}

// Summary totals a set of uses.
type Summary struct {
	Uses      int
	Repos     int
	Addresses []string       // distinct addresses, sorted
	Versions  []VersionCount // most used first
}

// VersionCount is how many uses ask for one version; "" is unpinned.
type VersionCount struct {
	Version string
	Uses    int
}

// Summarize totals uses: how many, in how many repos, and per version.
func Summarize(uses []Use) Summary {
	s := Summary{Uses: len(uses)}
	repos, addrs, versions := map[string]bool{}, map[string]bool{}, map[string]int{}
	for _, u := range uses {
		repos[u.Repo] = true
		addrs[u.Address] = true
		versions[u.Version]++
	}
	s.Repos = len(repos)
	for a := range addrs {
		s.Addresses = append(s.Addresses, a)
	}
	sort.Strings(s.Addresses)
	for v, n := range versions {
		s.Versions = append(s.Versions, VersionCount{Version: v, Uses: n})
	}
	sort.Slice(s.Versions, func(i, j int) bool {
		if s.Versions[i].Uses != s.Versions[j].Uses {
			return s.Versions[i].Uses > s.Versions[j].Uses
		}
		return s.Versions[i].Version < s.Versions[j].Version
	})
	return s
}

// AtLeastBehind returns the uses known to be at least n release lines behind.
func AtLeastBehind(uses []Use, n int) []Use {
	var out []Use
	for _, u := range uses {
		if u.BehindKnown && u.Behind >= n {
			out = append(out, u)
		}
	}
	return out
}
