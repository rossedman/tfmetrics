package sink

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rossedman/tfmetrics/internal/facts"
	"github.com/rossedman/tfmetrics/internal/metrics"
	"github.com/rossedman/tfmetrics/internal/source"
)

// SARIF writes one SARIF 2.1.0 log per repo, ready for GitHub code scanning
// or any SARIF viewer: parse diagnostics, unpinned remote module calls, git
// module calls pinned to a branch, and module calls behind their latest
// release, each at the file and line it is about.
type SARIF struct {
	Dir string
	// MinBehind is how many release lines behind a module call must be to be
	// reported; 0, the default, turns the outdated-module rule off. A real
	// org has thousands of calls one or two minor releases behind, which
	// would bury the other alerts. Drift is only known when versions were
	// looked up.
	MinBehind int
}

// SARIF rule IDs.
const (
	RuleParseError     = "tfmetrics/parse-error"
	RuleParseWarning   = "tfmetrics/parse-warning"
	RuleUnpinnedModule = "tfmetrics/unpinned-module"
	RuleBranchRef      = "tfmetrics/branch-ref"
	RuleOutdatedModule = "tfmetrics/outdated-module"
)

// sarifRules describes every rule, in the order they are listed in a log.
var sarifRules = []sarifRule{
	{ID: RuleParseError, Name: "ParseError", Level: "error",
		Short: "Terraform that does not parse",
		Help:  "The file has a syntax error, or repeats a block address declared earlier in the module. Terraform rejects the module, so tfmetrics counts it in tf_parse_errors and leaves what it cannot read out of the facts."},
	{ID: RuleParseWarning, Name: "ParseWarning", Level: "warning",
		Short: "A file tfmetrics could not fully read",
		Help:  "tfmetrics read the file with a warning, for example an OpenTofu-only file it counts but does not parse."},
	{ID: RuleUnpinnedModule, Name: "UnpinnedModule", Level: "warning",
		Short: "Remote module call with no version or ref",
		Help:  "Without a version (registry sources) or ?ref= (git sources), the next terraform init takes whatever the source holds then, so the same commit can plan differently from one day to the next. Pin an exact version or tag."},
	{ID: RuleBranchRef, Name: "BranchRef", Level: "warning",
		Short: "Git module call pinned to a branch",
		Help:  "A ?ref= that names a branch moves whenever the branch does. Pin a release tag or a commit SHA instead."},
	{ID: RuleOutdatedModule, Name: "OutdatedModule", Level: "note",
		Short: "Module call behind its latest release",
		Help:  "The version this call selects is behind the module's latest release by the number of major.minor release lines shown, as tf_module_versions_behind counts them."},
}

// sarifRule is one rule's description.
type sarifRule struct {
	ID, Name, Level, Short, Help string
}

// sarifLog is the subset of the SARIF 2.1.0 object model tfmetrics writes.
type sarifLog struct {
	Schema  string     `json:"$schema"`
	Version string     `json:"version"`
	Runs    []sarifRun `json:"runs"`
}

type sarifRun struct {
	Tool    sarifTool     `json:"tool"`
	Results []sarifResult `json:"results"`
}

type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}

type sarifDriver struct {
	Name           string                `json:"name"`
	InformationURI string                `json:"informationUri"`
	Rules          []sarifReportingDescr `json:"rules"`
}

type sarifReportingDescr struct {
	ID                   string        `json:"id"`
	Name                 string        `json:"name"`
	ShortDescription     sarifText     `json:"shortDescription"`
	FullDescription      sarifText     `json:"fullDescription"`
	Help                 sarifText     `json:"help"`
	DefaultConfiguration sarifRuleConf `json:"defaultConfiguration"`
}

type sarifRuleConf struct {
	Level string `json:"level"`
}

type sarifText struct {
	Text string `json:"text"`
}

type sarifResult struct {
	RuleID    string          `json:"ruleId"`
	RuleIndex int             `json:"ruleIndex"`
	Level     string          `json:"level"`
	Message   sarifText       `json:"message"`
	Locations []sarifLocation `json:"locations"`
}

type sarifLocation struct {
	PhysicalLocation sarifPhysical `json:"physicalLocation"`
}

type sarifPhysical struct {
	ArtifactLocation sarifArtifact `json:"artifactLocation"`
	Region           *sarifRegion  `json:"region,omitempty"`
}

type sarifArtifact struct {
	URI       string `json:"uri"`
	URIBaseID string `json:"uriBaseId"`
}

type sarifRegion struct {
	StartLine int `json:"startLine"`
}

// Emit writes <Dir>/<repo>.sarif for every repo with Terraform.
func (s *SARIF) Emit(_ context.Context, results []facts.ScanResult, m metrics.Metrics) error {
	if err := checkLocal(s.Dir); err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	drift := map[string]int{}
	for _, d := range m.Drift {
		drift[d.Repo+"\x00"+d.Module+"\x00"+d.Name] = d.Behind
	}
	for _, r := range results {
		if len(r.Modules) == 0 {
			continue
		}
		b, err := json.MarshalIndent(sarifFor(r, drift, s.MinBehind), "", "  ")
		if err != nil {
			return err
		}
		name := strings.TrimSuffix(facts.FileName(r.Repo), ".json") + ".sarif"
		if err := os.WriteFile(filepath.Join(s.Dir, name), append(b, '\n'), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// sarifFor builds one repo's log. drift maps repo, module path and call name
// to how far behind the call is.
func sarifFor(r facts.ScanResult, drift map[string]int, minBehind int) sarifLog {
	index := map[string]int{}
	driver := sarifDriver{Name: "tfmetrics", InformationURI: "https://github.com/rossedman/tfmetrics"}
	for i, rule := range sarifRules {
		index[rule.ID] = i
		driver.Rules = append(driver.Rules, sarifReportingDescr{
			ID: rule.ID, Name: rule.Name,
			ShortDescription:     sarifText{rule.Short},
			FullDescription:      sarifText{rule.Help},
			Help:                 sarifText{rule.Help},
			DefaultConfiguration: sarifRuleConf{rule.Level},
		})
	}
	results := []sarifResult{}
	add := func(rule, msg, file string, line int) {
		results = append(results, sarifResult{
			RuleID: rule, RuleIndex: index[rule], Level: sarifRules[index[rule]].Level,
			Message: sarifText{msg}, Locations: []sarifLocation{location(file, line)},
		})
	}
	for _, mod := range r.Modules {
		for _, d := range mod.Diagnostics {
			rule := RuleParseWarning
			if d.Severity == facts.SeverityError {
				rule = RuleParseError
			}
			msg := d.Summary
			if d.Detail != "" {
				msg += ": " + d.Detail
			}
			add(rule, msg, orModule(d.File, mod.Path), d.Line)
		}
		for _, c := range mod.ModuleCalls {
			if c.SourceKind == source.KindLocal {
				continue
			}
			file := orModule(c.File, mod.Path)
			switch {
			case c.Pinned == facts.PinnedNone:
				add(RuleUnpinnedModule, fmt.Sprintf("Module %q (%s) has no version or ref; pin an exact version or tag.", c.Name, c.Source), file, c.Line)
			case c.SourceKind == source.KindGit && c.Pinned == facts.PinnedRange:
				add(RuleBranchRef, fmt.Sprintf("Module %q follows the branch %q; pin a release tag or commit SHA.", c.Name, c.Version), file, c.Line)
			}
			if n, ok := drift[r.Repo+"\x00"+mod.Path+"\x00"+c.Name]; ok && minBehind > 0 && n >= minBehind {
				add(RuleOutdatedModule, fmt.Sprintf("Module %q (%s at %s) is %d release %s behind the latest.", c.Name, c.Address, c.Version, n, plural(n, "line")), file, c.Line)
			}
		}
	}
	sort.SliceStable(results, func(i, j int) bool {
		a, b := results[i].Locations[0].PhysicalLocation, results[j].Locations[0].PhysicalLocation
		if a.ArtifactLocation.URI != b.ArtifactLocation.URI {
			return a.ArtifactLocation.URI < b.ArtifactLocation.URI
		}
		return a.Region.line() < b.Region.line()
	})
	return sarifLog{
		Schema:  "https://json.schemastore.org/sarif-2.1.0.json",
		Version: "2.1.0",
		Runs:    []sarifRun{{Tool: sarifTool{Driver: driver}, Results: results}},
	}
}

// location points at a repo-relative file and, when known, a line.
func location(file string, line int) sarifLocation {
	l := sarifLocation{PhysicalLocation: sarifPhysical{ArtifactLocation: sarifArtifact{URI: file, URIBaseID: "%SRCROOT%"}}}
	if line > 0 {
		l.PhysicalLocation.Region = &sarifRegion{StartLine: line}
	}
	return l
}

// line returns the region's start line, or 0 for no region.
func (r *sarifRegion) line() int {
	if r == nil {
		return 0
	}
	return r.StartLine
}

// orModule returns file, or the module directory when the facts do not say
// which file (facts written before module calls carried a position).
func orModule(file, module string) string {
	if file != "" {
		return file
	}
	return path.Clean(module) + "/"
}

// plural returns noun, with an "s" unless n is 1.
func plural(n int, noun string) string {
	if n == 1 {
		return noun
	}
	return noun + "s"
}
