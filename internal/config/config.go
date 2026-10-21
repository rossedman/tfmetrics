// Package config defines tfmetrics.yaml and its defaults.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rossedman/tfmetrics/internal/classify"
)

// Config is the whole tfmetrics.yaml file.
type Config struct {
	Source  Source         `mapstructure:"source"`
	Scan    Scan           `mapstructure:"scan"`
	Modules Modules        `mapstructure:"modules"`
	Labels  Labels         `mapstructure:"labels"`
	Owners  Owners         `mapstructure:"owners"`
	Hygiene Hygiene        `mapstructure:"hygiene"`
	Sinks   []map[string]map[string]any `mapstructure:"sinks"`
}

// Source says where repos come from.
type Source struct {
	GitHub GitHub `mapstructure:"github"`
}

// GitHub configures discovery across one or more GitHub orgs.
type GitHub struct {
	TokenEnv string      `mapstructure:"token_env"`
	BaseURL  string      `mapstructure:"base_url"` // GitHub Enterprise API URL; empty for github.com
	CloneURL string      `mapstructure:"clone_url"`
	Defaults RepoDefault `mapstructure:"defaults"`
	Orgs     []Org       `mapstructure:"orgs"`
}

// RepoDefault sets which kinds of repos are included by default.
type RepoDefault struct {
	Archived bool `mapstructure:"archived"`
	Forks    bool `mapstructure:"forks"`
}

// Org is one GitHub org and its repo filters.
type Org struct {
	Name    string `mapstructure:"name"`
	Include Filter `mapstructure:"include"`
	Exclude Filter `mapstructure:"exclude"`
}

// Filter selects repos. Kinds are ANDed; values within a kind are ORed.
type Filter struct {
	Names      []string            `mapstructure:"names"`
	Topics     []string            `mapstructure:"topics"`
	Properties map[string][]string `mapstructure:"properties"`
}

// Empty reports whether the filter sets nothing.
func (f Filter) Empty() bool {
	return len(f.Names) == 0 && len(f.Topics) == 0 && len(f.Properties) == 0
}

// Scan configures scanning.
type Scan struct {
	Concurrency   int      `mapstructure:"concurrency"`
	IgnoreDirs    []string `mapstructure:"ignore_dirs"`
	CacheDir      string   `mapstructure:"cache_dir"`
	RootOverrides []string `mapstructure:"root_overrides"`
	Ref           string   `mapstructure:"ref"`
}

// Modules configures module source normalization and version lookups.
type Modules struct {
	InternalRegistry string            `mapstructure:"internal_registry"`
	RegistryTokenEnv string            `mapstructure:"registry_token_env"`
	Aliases          map[string]string `mapstructure:"aliases"`
}

// Labels configures extra metric labels.
type Labels struct {
	Properties []string `mapstructure:"properties"`
	Drop       []string `mapstructure:"drop"`
}

// Owners configures owner resolution.
type Owners struct {
	Property string `mapstructure:"property"`
}

// Hygiene configures the hygiene score.
type Hygiene struct {
	Weights map[string]float64 `mapstructure:"weights"`
}

// DefaultWeights are the hygiene weights from the spec.
var DefaultWeights = map[string]float64{
	"pinned":               30,
	"required_version":     15,
	"provider_constraints": 15,
	"typed_vars":           15,
	"described":            15,
	"parse_clean":          10,
}

// Default returns a config with every default applied.
func Default() Config {
	c := Config{}
	c.ApplyDefaults()
	return c
}

// ApplyDefaults fills unset fields with their defaults.
func (c *Config) ApplyDefaults() {
	if c.Source.GitHub.TokenEnv == "" {
		c.Source.GitHub.TokenEnv = "GITHUB_TOKEN"
	}
	if c.Scan.Concurrency <= 0 {
		c.Scan.Concurrency = 16
	}
	if c.Scan.IgnoreDirs == nil {
		c.Scan.IgnoreDirs = append([]string(nil), classify.DefaultIgnoreDirs...)
	}
	if c.Scan.CacheDir == "" {
		if d, err := os.UserCacheDir(); err == nil {
			c.Scan.CacheDir = filepath.Join(d, "tfmetrics")
		}
	}
	c.Scan.CacheDir = ExpandHome(c.Scan.CacheDir)
	if c.Owners.Property == "" {
		c.Owners.Property = "owner"
	}
	if len(c.Hygiene.Weights) == 0 {
		c.Hygiene.Weights = map[string]float64{}
		for k, v := range DefaultWeights {
			c.Hygiene.Weights[k] = v
		}
	}
}

// Validate checks values that defaults cannot fix.
func (c Config) Validate() error {
	for k := range c.Hygiene.Weights {
		if _, ok := DefaultWeights[k]; !ok {
			return fmt.Errorf("hygiene.weights: unknown key %q", k)
		}
	}
	for i, o := range c.Source.GitHub.Orgs {
		if strings.TrimSpace(o.Name) == "" {
			return fmt.Errorf("source.github.orgs[%d]: name is required", i)
		}
	}
	return nil
}

// ExpandHome replaces a leading ~ with the user's home directory.
func ExpandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}
