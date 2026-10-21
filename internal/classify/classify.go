// Package classify finds the Terraform module directories in a repo and
// decides whether each one is a root module or a child module.
package classify

import (
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// DefaultIgnoreDirs are skipped unless the config says otherwise.
var DefaultIgnoreDirs = []string{".terraform", "examples", "test"}

// IsTerraformFile reports whether a file name is Terraform configuration.
func IsTerraformFile(name string) bool {
	return strings.HasSuffix(name, ".tf") || strings.HasSuffix(name, ".tf.json") || strings.HasSuffix(name, ".tofu") || strings.HasSuffix(name, ".tofu.json")
}

// FindModuleDirs returns every directory under root that holds Terraform files,
// as slash-separated paths relative to root ("." for root itself), sorted.
// A directory whose base name or relative path matches an ignore pattern is
// skipped with everything below it, as are .git and other hidden directories.
func FindModuleDirs(root string, ignore []string) ([]string, error) {
	found := map[string]bool{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel != "." && (strings.HasPrefix(d.Name(), ".") || Ignored(rel, ignore)) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() && IsTerraformFile(d.Name()) {
			found[path.Dir(rel)] = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	dirs := make([]string, 0, len(found))
	for d := range found {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	return dirs, nil
}

// Ignored reports whether a relative directory matches any ignore pattern. A
// pattern without a slash matches the base name; one with a slash matches the
// whole relative path. Patterns use path.Match syntax.
func Ignored(rel string, patterns []string) bool {
	base := path.Base(rel)
	for _, p := range patterns {
		p = strings.Trim(p, "/")
		if p == "" {
			continue
		}
		target := base
		if strings.Contains(p, "/") {
			target = rel
		}
		if ok, _ := path.Match(p, target); ok {
			return true
		}
	}
	return false
}

// Signals are the facts that make a directory a root module.
type Signals struct {
	Backend            bool // has a backend block
	Cloud              bool // has a cloud block
	ConfiguredProvider bool // has a provider block with configuration
}

// Kind returns "root" when any signal is present or the directory matches a
// root override pattern, and "child" otherwise.
func Kind(rel string, s Signals, rootOverrides []string) string {
	if s.Backend || s.Cloud || s.ConfiguredProvider {
		return "root"
	}
	for _, p := range rootOverrides {
		if ok, _ := path.Match(strings.Trim(p, "/"), rel); ok {
			return "root"
		}
	}
	return "child"
}
