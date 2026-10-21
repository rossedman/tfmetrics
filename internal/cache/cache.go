// Package cache stores facts keyed by repo and commit SHA, so an unchanged
// repo reuses its last facts without cloning.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rossedman/tfmetrics/internal/facts"
)

// Cache is a directory of cached facts. A zero Dir disables caching.
type Cache struct {
	Dir string
}

// Key identifies one cached scan: the commit plus a hash of the repo metadata
// (topics, properties) and scan settings, so a change to any of them rescans.
func Key(commit string, topics []string, properties map[string]string, settings string) string {
	h := sha256.New()
	t := append([]string(nil), topics...)
	sort.Strings(t)
	b, _ := json.Marshal(struct {
		T []string
		P map[string]string
		S string
	}{t, properties, settings})
	h.Write(b)
	return commit + "-" + hex.EncodeToString(h.Sum(nil))[:12]
}

// path returns where facts for repo and key live.
func (c Cache) path(repo, key string) string {
	return filepath.Join(c.Dir, strings.TrimSuffix(facts.FileName(repo), ".json"), key+".json")
}

// Get returns cached facts, if any.
func (c Cache) Get(repo, key string) (facts.ScanResult, bool) {
	if c.Dir == "" || key == "" {
		return facts.ScanResult{}, false
	}
	r, err := facts.Read(c.path(repo, key))
	if err != nil {
		return facts.ScanResult{}, false
	}
	return r, true
}

// Put stores facts, replacing older entries for the same repo.
func (c Cache) Put(key string, r facts.ScanResult) error {
	if c.Dir == "" || key == "" {
		return nil
	}
	p := c.path(r.Repo, key)
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	old, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	for _, o := range old {
		_ = os.Remove(o)
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o644)
}
