// Package parse turns one Terraform module directory into ModuleFacts. It
// loads the standard model with terraform-config-inspect, then walks the raw
// HCL for the facts that model does not expose. It never evaluates anything,
// and parse errors become diagnostics rather than failures.
package parse

import (
	"bytes"
	"fmt"
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
	seen      map[string]hcl.Range // first declaration of each single-use block
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

	r, mod, hdiags := loadDir(dir, rel)
	m.Files, m.Lines = r.files, r.lines
	m.Backend = r.backend
	m.MovedBlocks, m.ImportBlocks = r.moved, r.imports
	m.Kind = classify.Kind(rel, r.signals, opts.RootOverrides)
	m.Diagnostics = append(m.Diagnostics, r.diags...)

	if mod != nil && !hdiags.HasErrors() {
		for _, d := range hdiags {
			m.Diagnostics = append(m.Diagnostics, convertHCLDiag(d))
		}
	} else {
		// Let terraform-config-inspect load the module itself: its legacy HCL1
		// parser reads some pre-0.12 modules that HCL2 rejects.
		var diags tfconfig.Diagnostics
		var err error
		mod, diags, err = loadModule(dir)
		if err != nil {
			m.Diagnostics = append(m.Diagnostics, facts.Diagnostic{
				Severity: facts.SeverityError, Summary: "Terraform parser crashed", Detail: err.Error(), File: rel,
			})
			return finish(m)
		}
		for _, d := range diags {
			m.Diagnostics = append(m.Diagnostics, convertDiag(d, repoRoot))
		}
		if mod == nil {
			return finish(m)
		}
	}

	m.RequiredVersion = strings.Join(mod.RequiredCore, ", ")
	m.Providers = providers(mod, r)
	m.Resources, m.Lifecycle = resources(mod.ManagedResources, r, "")
	m.DataSources, _ = resources(mod.DataResources, r, "data.")
	m.ModuleCalls = moduleCalls(mod.ModuleCalls, r, repoRoot, rel, opts.Normalizer)
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

// loadModule calls tfconfig.LoadModule and turns a panic inside it into an
// error. Its legacy HCL1 fallback panics on some malformed .tf.json files, and
// one bad file must not take down a scan of a whole org.
func loadModule(dir string) (mod *tfconfig.Module, diags tfconfig.Diagnostics, err error) {
	defer func() {
		if r := recover(); r != nil {
			mod, diags, err = nil, nil, fmt.Errorf("panic: %v", r)
		}
	}()
	mod, diags = tfconfig.LoadModuleFromFilesystem(moduleFS{}, dir)
	return mod, diags, nil
}

// moduleFS is the operating system's filesystem with Terraform Stacks files
// hidden. terraform-config-inspect reads .tfstack.hcl and .tfcomponent.hcl
// files as part of a module and panics on their blocks, but they are not
// module configuration.
type moduleFS struct{}

// Open opens a file.
func (moduleFS) Open(name string) (tfconfig.File, error) { return os.Open(name) }

// ReadFile reads a file.
func (moduleFS) ReadFile(name string) ([]byte, error) { return os.ReadFile(name) }

// ReadDir lists a directory without its stack files.
func (moduleFS) ReadDir(dir string) ([]os.FileInfo, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []os.FileInfo
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tfstack.hcl") || strings.HasSuffix(e.Name(), ".tfcomponent.hcl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		out = append(out, info)
	}
	return out, nil
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
func moduleCalls(in map[string]*tfconfig.ModuleCall, r raw, repoRoot, rel string, n *source.Normalizer) []facts.ModuleCall {
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
			File:       relFile(repoRoot, c.Pos.Filename),
			Line:       c.Pos.Line,
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

// convertHCLDiag converts a diagnostic from parsing a file whose name is
// already repo-relative, the way tfconfig and convertDiag together would.
func convertHCLDiag(d *hcl.Diagnostic) facts.Diagnostic {
	out := facts.Diagnostic{Severity: facts.SeverityWarning, Summary: d.Summary, Detail: d.Detail}
	if d.Severity == hcl.DiagError {
		out.Severity = facts.SeverityError
	}
	if d.Subject != nil {
		out.File = d.Subject.Filename
		out.Line = d.Subject.Start.Line
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
		{Type: "variable", LabelNames: []string{"name"}},
		{Type: "output", LabelNames: []string{"name"}},
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

// loadDir parses each file in dir once. The Terraform files feed the raw
// facts, and the files terraform-config-inspect would read build its module,
// in its order: primary files, then overrides. mod is nil when the directory
// cannot be read or tfconfig panics; diags are the parse and decode
// diagnostics for the module, which the caller only trusts without errors.
func loadDir(dir, rel string) (r raw, mod *tfconfig.Module, diags hcl.Diagnostics) {
	r = raw{providers: map[string]bool{}, blocks: map[string]rawBlock{}, seen: map[string]hcl.Range{}}
	entries, err := os.ReadDir(dir)
	if err != nil {
		r.diags = append(r.diags, facts.Diagnostic{Severity: facts.SeverityError, Summary: "cannot read module directory", Detail: err.Error(), File: rel})
		return r, nil, nil
	}
	p := hclparse.NewParser()
	type parsed struct {
		file  *hcl.File
		diags hcl.Diagnostics
	}
	files := map[string]parsed{}
	parse := func(name string, src []byte) parsed {
		fileRel := path.Join(rel, name)
		var pf parsed
		if strings.HasSuffix(name, ".json") {
			pf.file, pf.diags = p.ParseJSON(src, fileRel)
		} else {
			pf.file, pf.diags = p.ParseHCL(src, fileRel)
		}
		files[name] = pf
		return pf
	}
	var primary, override []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			continue
		}
		if ext := tfconfigExt(name); ext != "" && !tfconfigIgnored(name) {
			base := strings.TrimSuffix(name, ext)
			if base == "override" || strings.HasSuffix(base, "_override") {
				override = append(override, name)
			} else {
				primary = append(primary, name)
			}
		}
		if !classify.IsTerraformFile(name) {
			continue
		}
		fileRel := path.Join(rel, name)
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
		if pf := parse(name, src); pf.file != nil && pf.file.Body != nil {
			r.collect(pf.file.Body)
		}
	}

	defer func() {
		if recover() != nil {
			mod, diags = nil, nil // the caller falls back to loadModule
		}
	}()
	mod = tfconfig.NewModule(dir)
	for _, name := range append(primary, override...) {
		pf, ok := files[name]
		if !ok {
			src, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				return r, nil, nil
			}
			pf = parse(name, src)
		}
		diags = append(diags, pf.diags...)
		if pf.file != nil {
			diags = append(diags, tfconfig.LoadModuleFromFile(pf.file, mod)...)
		}
	}
	// What tfconfig's LoadModule adds after loading: a requirement for every
	// provider a resource implies.
	for _, res := range mod.ManagedResources {
		if _, ok := mod.RequiredProviders[res.Provider.Name]; !ok {
			mod.RequiredProviders[res.Provider.Name] = &tfconfig.ProviderRequirement{}
		}
	}
	for _, res := range mod.DataResources {
		if _, ok := mod.RequiredProviders[res.Provider.Name]; !ok {
			mod.RequiredProviders[res.Provider.Name] = &tfconfig.ProviderRequirement{}
		}
	}
	return r, mod, diags
}

// tfconfigExt returns the extension of a module file terraform-config-inspect
// loads, or "" for any other file. Stacks files are left out; see moduleFS.
func tfconfigExt(name string) string {
	for _, ext := range []string{".tf", ".tf.json"} {
		if strings.HasSuffix(name, ext) {
			return ext
		}
	}
	return ""
}

// tfconfigIgnored reports whether terraform-config-inspect skips name as a
// hidden or editor backup file.
func tfconfigIgnored(name string) bool {
	return strings.HasPrefix(name, ".") || strings.HasSuffix(name, "~") ||
		strings.HasPrefix(name, "#") && strings.HasSuffix(name, "#")
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
			r.declare(b, b.Labels[0]+"."+b.Labels[1])
			r.blocks[b.Labels[0]+"."+b.Labels[1]] = repeatFacts(b.Body)
		case "data":
			r.declare(b, "data."+b.Labels[0]+"."+b.Labels[1])
			r.blocks["data."+b.Labels[0]+"."+b.Labels[1]] = repeatFacts(b.Body)
		case "module":
			r.declare(b, "module."+b.Labels[0])
			r.blocks["module."+b.Labels[0]] = repeatFacts(b.Body)
		case "variable":
			r.declare(b, "var."+b.Labels[0])
		case "output":
			r.declare(b, "output."+b.Labels[0])
		case "moved":
			r.moved++
		case "import":
			r.imports++
		}
	}
}

// duplicateSummary names each block type Terraform allows only once per
// address in a module.
var duplicateSummary = map[string]string{
	"resource": "Duplicate resource",
	"data":     "Duplicate data source",
	"module":   "Duplicate module call",
	"variable": "Duplicate variable",
	"output":   "Duplicate output",
}

// declare records the block at addr and reports a second declaration as an
// error. Terraform rejects duplicates, and terraform-config-inspect keeps only
// one of them without saying so.
func (r *raw) declare(b *hcl.Block, addr string) {
	first, ok := r.seen[addr]
	if !ok {
		r.seen[addr] = b.DefRange
		return
	}
	r.diags = append(r.diags, facts.Diagnostic{
		Severity: facts.SeverityError,
		Summary:  duplicateSummary[b.Type],
		Detail:   fmt.Sprintf("%s is already declared at %s:%d; only one declaration is counted", addr, first.Filename, first.Start.Line),
		File:     b.DefRange.Filename,
		Line:     b.DefRange.Start.Line,
	})
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
