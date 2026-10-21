// Package scan turns one checked-out repo into a ScanResult: classify every
// module directory, parse it, and attach repo metadata and owners.
package scan

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/rossedman/tfmetrics/internal/classify"
	"github.com/rossedman/tfmetrics/internal/facts"
	"github.com/rossedman/tfmetrics/internal/owners"
	"github.com/rossedman/tfmetrics/internal/parse"
	"github.com/rossedman/tfmetrics/internal/source"
)

// Meta is what is known about a repo before it is parsed.
type Meta struct {
	Repo       string // org/name
	Org        string
	Topics     []string
	Properties map[string]string
	Commit     string // detected from git when empty
}

// Options control a scan.
type Options struct {
	IgnoreDirs    []string
	RootOverrides []string
	Normalizer    *source.Normalizer
	OwnerProperty string
	Now           func() time.Time
}

// Dir scans the repo checked out at root.
func Dir(ctx context.Context, root string, meta Meta, opts Options) (facts.ScanResult, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return facts.ScanResult{}, err
	}
	if meta.Repo == "" {
		meta.Repo = RepoName(ctx, abs)
	}
	if meta.Org == "" {
		if org, _, ok := strings.Cut(meta.Repo, "/"); ok {
			meta.Org = org
		}
	}
	if meta.Commit == "" {
		meta.Commit = HeadCommit(ctx, abs)
	}
	dirs, err := classify.FindModuleDirs(abs, opts.IgnoreDirs)
	if err != nil {
		return facts.ScanResult{}, err
	}
	res := facts.ScanResult{
		SchemaVersion: facts.SchemaVersion,
		Repo:          meta.Repo,
		Org:           meta.Org,
		Topics:        nonNil(meta.Topics),
		Properties:    meta.Properties,
		Commit:        meta.Commit,
		ScannedAt:     opts.Now().UTC().Truncate(time.Second),
		Owners:        owners.Resolve(abs, meta.Properties, opts.OwnerProperty, meta.Topics),
		Modules:       make([]facts.ModuleFacts, 0, len(dirs)),
	}
	if res.Properties == nil {
		res.Properties = map[string]string{}
	}
	popts := parse.Options{Normalizer: opts.Normalizer, RootOverrides: opts.RootOverrides}
	for _, d := range dirs {
		if err := ctx.Err(); err != nil {
			return facts.ScanResult{}, err
		}
		res.Modules = append(res.Modules, parse.Module(abs, d, popts))
	}
	return res, nil
}

// nonNil returns s, or an empty slice when s is nil.
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// HeadCommit returns the HEAD commit of the git repo at dir, or "" if dir is
// not a git checkout.
func HeadCommit(ctx context.Context, dir string) string {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// RepoName derives org/name for a local checkout from its origin remote, or
// "local/<dir name>" when there is no usable remote.
func RepoName(ctx context.Context, dir string) string {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "remote", "get-url", "origin").Output()
	if err == nil {
		if name := RepoFromURL(strings.TrimSpace(string(out))); name != "" {
			return name
		}
	}
	return "local/" + filepath.Base(dir)
}

// RepoFromURL extracts org/name from a git remote URL.
func RepoFromURL(u string) string {
	u = strings.TrimSuffix(strings.TrimSuffix(u, "/"), ".git")
	if strings.HasPrefix(u, "/") || strings.HasPrefix(u, ".") {
		return "" // a filesystem path, not a remote with an org
	}
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	} else if at := strings.Index(u, "@"); at >= 0 {
		u = strings.Replace(u[at+1:], ":", "/", 1)
	}
	parts := strings.Split(u, "/")
	if len(parts) < 3 {
		return ""
	}
	return parts[len(parts)-2] + "/" + parts[len(parts)-1]
}
