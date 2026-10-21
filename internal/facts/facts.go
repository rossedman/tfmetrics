// Package facts defines the facts file: the per-repo record that every metric
// is derived from, and the contract between scanning and aggregation.
package facts

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SchemaVersion is the version of the facts file format written by this build.
const SchemaVersion = 1

// Module kinds.
const (
	KindRoot  = "root"
	KindChild = "child"
)

// Pinning levels for a module call.
const (
	PinnedExact = "exact"
	PinnedRange = "range"
	PinnedNone  = "none"
)

// Diagnostic severities.
const (
	SeverityError   = "error"
	SeverityWarning = "warning"
)

// ScanResult is the facts for one repo at one commit.
type ScanResult struct {
	SchemaVersion int               `json:"schema_version"`
	Repo          string            `json:"repo"` // org/name
	Org           string            `json:"org"`
	Topics        []string          `json:"topics"`
	Properties    map[string]string `json:"properties"` // GitHub custom properties at scan time
	Commit        string            `json:"commit"`
	ScannedAt     time.Time         `json:"scanned_at"`
	Owners        []string          `json:"owners"` // from CODEOWNERS / custom property / repo topics
	Modules       []ModuleFacts     `json:"modules"`
}

// ModuleFacts is everything tfmetrics knows about one module directory.
type ModuleFacts struct {
	Path            string         `json:"path"` // relative to repo root
	Kind            string         `json:"kind"` // root | child
	Files           int            `json:"files"`
	Lines           int            `json:"lines"`
	RequiredVersion string         `json:"required_version"`
	Backend         string         `json:"backend"` // s3, gcs, remote, cloud, ""
	Providers       []ProviderReq  `json:"providers"`
	Resources       []BlockRef     `json:"resources"`
	DataSources     []BlockRef     `json:"data_sources"`
	ModuleCalls     []ModuleCall   `json:"module_calls"`
	Variables       []VarFacts     `json:"variables"`
	Outputs         []OutputFacts  `json:"outputs"`
	Lifecycle       LifecycleFacts `json:"lifecycle"`
	MovedBlocks     int            `json:"moved_blocks"`
	ImportBlocks    int            `json:"import_blocks"`
	Diagnostics     []Diagnostic   `json:"diagnostics"`
}

// ProviderReq is one entry of required_providers.
type ProviderReq struct {
	Name       string `json:"name"`       // local name, e.g. aws
	Source     string `json:"source"`     // e.g. hashicorp/aws, "" if not declared
	Constraint string `json:"constraint"` // version constraint, "" if none
}

// BlockRef is one resource or data block.
type BlockRef struct {
	Type           string `json:"type"`
	Name           string `json:"name"`
	Provider       string `json:"provider"`
	Count          bool   `json:"count,omitempty"`
	ForEach        bool   `json:"for_each,omitempty"`
	PreventDestroy bool   `json:"prevent_destroy,omitempty"`
}

// ModuleCall is one module block.
type ModuleCall struct {
	Name       string `json:"name"`
	Source     string `json:"source"`      // as written
	SourceKind string `json:"source_kind"` // local | registry | private_registry | git | http | s3 | gcs | unknown
	Address    string `json:"address"`     // normalized, e.g. app.terraform.io/acme/vpc/aws
	Version    string `json:"version"`     // constraint or git ref, "" if unpinned
	Pinned     string `json:"pinned"`      // exact | range | none
	Count      bool   `json:"count,omitempty"`
	ForEach    bool   `json:"for_each,omitempty"`
}

// VarFacts is one variable block.
type VarFacts struct {
	Name           string `json:"name"`
	HasType        bool   `json:"has_type"`
	HasDescription bool   `json:"has_description"`
	Sensitive      bool   `json:"sensitive"`
	HasDefault     bool   `json:"has_default"`
}

// OutputFacts is one output block.
type OutputFacts struct {
	Name           string `json:"name"`
	HasDescription bool   `json:"has_description"`
	Sensitive      bool   `json:"sensitive"`
}

// LifecycleFacts counts lifecycle settings across a module's resources.
type LifecycleFacts struct {
	PreventDestroy      int `json:"prevent_destroy"`
	IgnoreChanges       int `json:"ignore_changes"`
	CreateBeforeDestroy int `json:"create_before_destroy"`
}

// Diagnostic is a parse error or warning. Parse problems are facts, never fatal.
type Diagnostic struct {
	Severity string `json:"severity"` // error | warning
	Summary  string `json:"summary"`
	Detail   string `json:"detail,omitempty"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
}

// ParseErrorFiles returns the number of distinct files with an error diagnostic.
// A diagnostic without a file counts as one file.
func (m ModuleFacts) ParseErrorFiles() int {
	seen := map[string]bool{}
	n := 0
	for _, d := range m.Diagnostics {
		if d.Severity != SeverityError {
			continue
		}
		if d.File == "" {
			n++
			continue
		}
		if !seen[d.File] {
			seen[d.File] = true
			n++
		}
	}
	return n
}

// FileName returns the facts file name for a repo: org/name becomes org__name.json.
func FileName(repo string) string {
	r := strings.NewReplacer("/", "__", "\\", "__", ":", "_")
	return r.Replace(repo) + ".json"
}

// Write writes a ScanResult as indented JSON into dir and returns the path.
func Write(dir string, r ScanResult) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, FileName(r.Repo))
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	return path, os.WriteFile(path, append(b, '\n'), 0o644)
}

// Read reads one facts file and checks its schema version.
func Read(path string) (ScanResult, error) {
	var r ScanResult
	b, err := os.ReadFile(path)
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return r, fmt.Errorf("%s: %w", path, err)
	}
	if r.SchemaVersion < 1 || r.SchemaVersion > SchemaVersion {
		return r, fmt.Errorf("%s: unsupported schema_version %d (this build reads 1..%d)", path, r.SchemaVersion, SchemaVersion)
	}
	return r, nil
}

// ReadAll reads facts from each path. A directory contributes every *.json file
// in it (not recursive); a file is read as is. Results are sorted by repo.
func ReadAll(paths []string) ([]ScanResult, error) {
	var files []string
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			files = append(files, p)
			continue
		}
		matches, err := filepath.Glob(filepath.Join(p, "*.json"))
		if err != nil {
			return nil, err
		}
		for _, m := range matches {
			if filepath.Base(m) == "metrics.json" {
				continue
			}
			files = append(files, m)
		}
	}
	out := make([]ScanResult, 0, len(files))
	for _, f := range files {
		r, err := Read(f)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Repo < out[j].Repo })
	return out, nil
}
