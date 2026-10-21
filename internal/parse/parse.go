// Package parse turns one Terraform module directory into ModuleFacts. It
// loads the standard model with terraform-config-inspect, then walks the raw
// HCL for the facts that model does not expose. It never evaluates anything,
// and parse errors become diagnostics rather than failures.
package parse

import (
	"bytes"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/terraform-config-inspect/tfconfig"
	"github.com/zclconf/go-cty/cty"

	"github.com/rossedman/tfmetrics/internal/classify"
	"github.com/rossedman/tfmetrics/internal/facts"
	"github.com/rossedman/tfmetrics/internal/source"
)

// Options control how a module is parsed.
type Options struct {
	Normalizer    *source.Normalizer
	RootOverrides []string // path.Match patterns of directories to treat as roots
}

// rawBlock holds raw facts about one resource, data or module block.
type rawBlock struct {
	count, forEach                bool
	preventDestroy, cbd, ignoreCh bool
}

// raw holds the facts gathered from walking the HCL directly.
type raw struct {
	signals   classify.Signals
	backend   string
	providers map[string]bool // provider block names
	blocks    map[string]rawBlock
	moved     int
	imports   int
	files     int
	lines     int
	diags     []facts.Diagnostic
}

// Module parses the module directory rel (slash-separated, relative to
// repoRoot) and returns its facts.
func Module(repoRoot, rel string, opts Options) facts.ModuleFacts {
	if opts.Normalizer == nil {
		opts.Normalizer = source.NewNormalizer("", nil)
	}
	dir := filepath.Join(repoRoot, filepath.FromSlash(rel))
	m := facts.ModuleFacts{Path: rel}

	r := walkRaw(dir, rel)
	m.Files, m.Lines = r.files, r.lines
	m.Backend = r.backend
	m.MovedBlocks, m.ImportBlocks = r.moved, r.imports
	m.Kind = classify.Kind(rel, r.signals, opts.RootOverrides)
	m.Diagnostics = append(m.Diagnostics, r.diags...)

	mod, diags := tfconfig.LoadModule(dir)
	for _, d := range diags {
		m.Diagnostics = append(m.Diagnostics, convertDiag(d, repoRoot))
	}
	if mod == nil {
		return finish(m)
	}

	m.RequiredVersion = strings.Join(mod.RequiredCore, ", ")
	m.Providers = providers(mod, r)
	m.Resources, m.Lifecycle = resources(mod.ManagedResources, r, "")
	m.DataSources, _ = resources(mod.DataResources, r, "data.")
	m.ModuleCalls = moduleCalls(mod.ModuleCalls, r, rel, opts.Normalizer)
	for _, v := range mod.Variables {
		m.Variables = append(m.Variables, facts.VarFacts{
			Name:           v.Name,
			HasType:        v.Type != "",
			HasDescription: v.Description != "",
			Sensitive:      v.Sensitive,
			HasDefault:     !v.Required,
		})
	}
	for _, o := range mod.Outputs {
		m.Outputs = append(m.Outputs, facts.OutputFacts{
			Name:           o.Name,
			HasDescription: o.Description != "",
			Sensitive:      o.Sensitive,
		})
	}
	return finish(m)
}

// finish sorts every slice so output is deterministic, and replaces nil slices
// with empty ones so the JSON has [] rather than null.
func finish(m facts.ModuleFacts) facts.ModuleFacts {
	sort.Slice(m.Providers, func(i, j int) bool { return m.Providers[i].Name < m.Providers[j].Name })
	sortRefs(m.Resources)
	sortRefs(m.DataSources)
	sort.Slice(m.ModuleCalls, func(i, j int) bool { return m.ModuleCalls[i].Name < m.ModuleCalls[j].Name })
	sort.Slice(m.Variables, func(i, j int) bool { return m.Variables[i].Name < m.Variables[j].Name })
	sort.Slice(m.Outputs, func(i, j int) bool { return m.Outputs[i].Name < m.Outputs[j].Name })
	sort.SliceStable(m.Diagnostics, func(i, j int) bool {
		a, b := m.Diagnostics[i], m.Diagnostics[j]
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Line < b.Line
	})
	if m.Providers == nil {
		m.Providers = []facts.ProviderReq{}
	}
	if m.Resources == nil {
		m.Resources = []facts.BlockRef{}
	}
	if m.DataSources == nil {
		m.DataSources = []facts.BlockRef{}
	}
	if m.ModuleCalls == nil {
		m.ModuleCalls = []facts.ModuleCall{}
	}
	if m.Variables == nil {
		m.Variables = []facts.VarFacts{}
	}
	if m.Outputs == nil {
		m.Outputs = []facts.OutputFacts{}
	}
	if m.Diagnostics == nil {
		m.Diagnostics = []facts.Diagnostic{}
	}
	return m
}

// sortRefs sorts block references by type then name.
func sortRefs(refs []facts.BlockRef) {
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Type != refs[j].Type {
			return refs[i].Type < refs[j].Type
		}
		return refs[i].Name < refs[j].Name
	})
}

// providers merges required_providers with providers that are used or
// configured but not required, which get an empty constraint.
func providers(mod *tfconfig.Module, r raw) []facts.ProviderReq {
	seen := map[string]bool{}
	var out []facts.ProviderReq
	for name, req := range mod.RequiredProviders {
		seen[name] = true
		out = append(out, facts.ProviderReq{
			Name:       name,
			Source:     req.Source,
			Constraint: strings.Join(req.VersionConstraints, ", "),
		})
	}
	add := func(name string) {
		if name == "" || name == "terraform" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, facts.ProviderReq{Name: name})
	}
	for name := range r.providers {
		add(name)
	}
	for _, res := range mod.ManagedResources {
		add(res.Provider.Name)
	}
	for _, res := range mod.DataResources {
		add(res.Provider.Name)
	}
	return out
}

// resources converts tfconfig resources and joins the raw facts for each.
func resources(in map[string]*tfconfig.Resource, r raw, prefix string) ([]facts.BlockRef, facts.LifecycleFacts) {
	var out []facts.BlockRef
	var lc facts.LifecycleFacts
	for _, res := range in {
		rb := r.blocks[prefix+res.Type+"."+res.Name]
		out = append(out, facts.BlockRef{
			Type:           res.Type,
			Name:           res.Name,
			Provider:       res.Provider.Name,
			Count:          rb.count,
			ForEach:        rb.forEach,
			PreventDestroy: rb.preventDestroy,
		})
		if rb.preventDestroy {
			lc.PreventDestroy++
		}
		if rb.ignoreCh {
			lc.IgnoreChanges++
		}
		if rb.cbd {
			lc.CreateBeforeDestroy++
		}
	}
	return out, lc
}

// moduleCalls converts tfconfig module calls, classifying each source.
func moduleCalls(in map[string]*tfconfig.ModuleCall, r raw, rel string, n *source.Normalizer) []facts.ModuleCall {
	var out []facts.ModuleCall
	for _, c := range in {
		ref := n.Classify(c.Source)
		if ref.Kind == source.KindLocal {
			// Local paths are relative to the caller; report them repo-relative
			// so every caller of the same module shares one address.
			ref.Address = path.Join(rel, c.Source)
		}
		rb := r.blocks["module."+c.Name]
		out = append(out, facts.ModuleCall{
			Name:       c.Name,
			Source:     c.Source,
			SourceKind: ref.Kind,
			Address:    ref.Address,
			Version:    source.Version(ref, c.Version),
			Pinned:     source.Pinning(ref, c.Version),
			Count:      rb.count,
			ForEach:    rb.forEach,
		})
	}
	return out
}

// convertDiag converts a tfconfig diagnostic, making its file repo-relative.
func convertDiag(d tfconfig.Diagnostic, repoRoot string) facts.Diagnostic {
	out := facts.Diagnostic{Severity: facts.SeverityWarning, Summary: d.Summary, Detail: d.Detail}
	if d.Severity == tfconfig.DiagError {
		out.Severity = facts.SeverityError
	}
	if d.Pos != nil {
		out.File = relFile(repoRoot, d.Pos.Filename)
		out.Line = d.Pos.Line
	}
	return out
}

// relFile returns filename relative to repoRoot with forward slashes.
func relFile(repoRoot, filename string) string {
	if rel, err := filepath.Rel(repoRoot, filename); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(filename)
}

var (
	topSchema = &hcl.BodySchema{Blocks: []hcl.BlockHeaderSchema{
		{Type: "terraform"},
		{Type: "provider", LabelNames: []string{"name"}},
		{Type: "resource", LabelNames: []string{"type", "name"}},
		{Type: "data", LabelNames: []string{"type", "name"}},
		{Type: "module", LabelNames: []string{"name"}},
		{Type: "moved"},
		{Type: "import"},
	}}
	terraformSchema = &hcl.BodySchema{Blocks: []hcl.BlockHeaderSchema{
		{Type: "backend", LabelNames: []string{"type"}},
		{Type: "cloud"},
	}}
	repeatSchema = &hcl.BodySchema{
		Attributes: []hcl.AttributeSchema{{Name: "count"}, {Name: "for_each"}},
		Blocks:     []hcl.BlockHeaderSchema{{Type: "lifecycle"}},
	}
	lifecycleSchema = &hcl.BodySchema{Attributes: []hcl.AttributeSchema{
		{Name: "prevent_destroy"}, {Name: "ignore_changes"}, {Name: "create_before_destroy"},
	}}
)

// walkRaw parses every Terraform file in dir and gathers the raw facts.
func walkRaw(dir, rel string) raw {
	r := raw{providers: map[string]bool{}, blocks: map[string]rawBlock{}}
	entries, err := os.ReadDir(dir)
	if err != nil {
		r.diags = append(r.diags, facts.Diagnostic{Severity: facts.SeverityError, Summary: "cannot read module directory", Detail: err.Error(), File: rel})
		return r
	}
	p := hclparse.NewParser()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !classify.IsTerraformFile(name) {
			continue
		}
		fileRel := path.Join(rel, name)
		if rel == "." {
			fileRel = name
		}
		src, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			r.diags = append(r.diags, facts.Diagnostic{Severity: facts.SeverityError, Summary: "cannot read file", Detail: err.Error(), File: fileRel})
			continue
		}
		r.files++
		r.lines += countLines(src)
		if strings.HasSuffix(name, ".tofu") || strings.HasSuffix(name, ".tofu.json") {
			r.diags = append(r.diags, facts.Diagnostic{Severity: facts.SeverityWarning, Summary: "OpenTofu file not parsed", Detail: "tfmetrics counts .tofu files but parses only .tf and .tf.json", File: fileRel})
			continue
		}
		var f *hcl.File
		var diags hcl.Diagnostics
		if strings.HasSuffix(name, ".json") {
			f, diags = p.ParseJSON(src, fileRel)
		} else {
			f, diags = p.ParseHCL(src, fileRel)
		}
		// tfconfig reports syntax errors itself; record only what it cannot.
		_ = diags
		if f == nil || f.Body == nil {
			continue
		}
		r.collect(f.Body)
	}
	return r
}

// collect gathers raw facts from one file body.
func (r *raw) collect(body hcl.Body) {
	content, _, _ := body.PartialContent(topSchema)
	if content == nil {
		return
	}
	for _, b := range content.Blocks {
		switch b.Type {
		case "terraform":
			tc, _, _ := b.Body.PartialContent(terraformSchema)
			if tc == nil {
				continue
			}
			for _, tb := range tc.Blocks {
				switch tb.Type {
				case "backend":
					r.signals.Backend = true
					r.backend = tb.Labels[0]
				case "cloud":
					r.signals.Cloud = true
					r.backend = "cloud"
				}
			}
		case "provider":
			r.providers[b.Labels[0]] = true
			if providerConfigured(b.Body) {
				r.signals.ConfiguredProvider = true
			}
		case "resource":
			r.blocks[b.Labels[0]+"."+b.Labels[1]] = repeatFacts(b.Body)
		case "data":
			r.blocks["data."+b.Labels[0]+"."+b.Labels[1]] = repeatFacts(b.Body)
		case "module":
			r.blocks["module."+b.Labels[0]] = repeatFacts(b.Body)
		case "moved":
			r.moved++
		case "import":
			r.imports++
		}
	}
}

// providerConfigured reports whether a provider block sets anything beyond
// alias and version.
func providerConfigured(body hcl.Body) bool {
	if sb, ok := body.(*hclsyntax.Body); ok {
		if len(sb.Blocks) > 0 {
			return true
		}
		for name := range sb.Attributes {
			if name != "alias" && name != "version" {
				return true
			}
		}
		return false
	}
	attrs, diags := body.JustAttributes()
	if diags.HasErrors() {
		return true // nested blocks in JSON
	}
	for name := range attrs {
		if name != "alias" && name != "version" {
			return true
		}
	}
	return false
}

// repeatFacts reads count, for_each and lifecycle from a block body.
func repeatFacts(body hcl.Body) rawBlock {
	var rb rawBlock
	c, _, _ := body.PartialContent(repeatSchema)
	if c == nil {
		return rb
	}
	_, rb.count = c.Attributes["count"]
	_, rb.forEach = c.Attributes["for_each"]
	for _, lb := range c.Blocks {
		lc, _, _ := lb.Body.PartialContent(lifecycleSchema)
		if lc == nil {
			continue
		}
		rb.preventDestroy = rb.preventDestroy || literalTrue(lc.Attributes["prevent_destroy"])
		rb.cbd = rb.cbd || literalTrue(lc.Attributes["create_before_destroy"])
		if _, ok := lc.Attributes["ignore_changes"]; ok {
			rb.ignoreCh = true
		}
	}
	return rb
}

// literalTrue reports whether an attribute is the literal value true.
func literalTrue(a *hcl.Attribute) bool {
	if a == nil {
		return false
	}
	v, diags := a.Expr.Value(nil)
	if diags.HasErrors() || v.IsNull() || !v.IsKnown() || v.Type() != cty.Bool {
		return false
	}
	return v.True()
}

// countLines counts lines, including a final line without a newline.
func countLines(b []byte) int {
	if len(b) == 0 {
		return 0
	}
	n := bytes.Count(b, []byte{'\n'})
	if b[len(b)-1] != '\n' {
		n++
	}
	return n
}
