// Package app wires configuration into the pipeline stages, so cobra commands
// stay thin.
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/viper"

	"github.com/rossedman/tfmetrics/internal/config"
	"github.com/rossedman/tfmetrics/internal/facts"
	"github.com/rossedman/tfmetrics/internal/metrics"
	"github.com/rossedman/tfmetrics/internal/registry"
	"github.com/rossedman/tfmetrics/internal/scan"
	"github.com/rossedman/tfmetrics/internal/sink"
	"github.com/rossedman/tfmetrics/internal/source"
)

// LoadConfig decodes the merged config (file, env, flags) from v and applies
// defaults.
func LoadConfig(v *viper.Viper) (config.Config, error) {
	var c config.Config
	if err := v.Unmarshal(&c); err != nil {
		return c, fmt.Errorf("config: %w", err)
	}
	c.ApplyDefaults()
	return c, c.Validate()
}

// Normalizer builds the module source normalizer from config.
func Normalizer(c config.Config) *source.Normalizer {
	return source.NewNormalizer(c.Modules.InternalRegistry, c.Modules.Aliases)
}

// ScanOptions builds scan options from config.
func ScanOptions(c config.Config) scan.Options {
	return scan.Options{
		IgnoreDirs:    c.Scan.IgnoreDirs,
		RootOverrides: c.Scan.RootOverrides,
		Normalizer:    Normalizer(c),
		OwnerProperty: c.Owners.Property,
	}
}

// Settings fingerprints everything that changes scan output for the same
// commit, for cache keys.
func Settings(c config.Config, buildVersion string) string {
	b, _ := json.Marshal(struct {
		Schema  int
		Version string
		Ignore  []string
		Roots   []string
		Reg     string
		Aliases map[string]string
		Owner   string
	}{facts.SchemaVersion, buildVersion, c.Scan.IgnoreDirs, c.Scan.RootOverrides, c.Modules.InternalRegistry, c.Modules.Aliases, c.Owners.Property})
	return string(b)
}

// GitHubToken returns the GitHub token from the configured environment variable.
func GitHubToken(c config.Config) string {
	return os.Getenv(c.Source.GitHub.TokenEnv)
}

// Resolver builds a version resolver that authenticates to the private
// registry and to GitHub for git tag lookups.
func Resolver(c config.Config) *registry.Resolver {
	r := registry.New()
	r.Tokens = map[string]string{}
	if c.Modules.InternalRegistry != "" && c.Modules.RegistryTokenEnv != "" {
		r.Tokens[strings.ToLower(c.Modules.InternalRegistry)] = os.Getenv(c.Modules.RegistryTokenEnv)
	}
	if t := GitHubToken(c); t != "" {
		r.GitToken, r.GitTokenHost = t, "github.com"
	}
	return r
}

// Aggregate computes metrics. With lister nil, versions_behind is skipped.
func Aggregate(ctx context.Context, c config.Config, results []facts.ScanResult, lister metrics.VersionLister, warn io.Writer) metrics.Metrics {
	opts := metrics.Options{
		PropertyLabels: c.Labels.Properties,
		DropLabels:     c.Labels.Drop,
		Weights:        c.Hygiene.Weights,
	}
	if lister != nil {
		opts.Versions = lister
		opts.OnLookupError = func(addr string, err error) {
			fmt.Fprintf(warn, "warning: version lookup for %s: %v\n", addr, err)
		}
	}
	return metrics.Compute(ctx, results, opts)
}

// SinkSpecs returns the sinks named on the command line, or the configured
// sinks when none are named. Options for a named sink come from the config
// entry of the same name, if any.
func SinkSpecs(c config.Config, names []string) ([]sink.Spec, error) {
	configured, err := sink.ParseSpecs(c.Sinks)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return configured, nil
	}
	var out []sink.Spec
	for _, n := range names {
		spec := sink.Spec{Name: n}
		for _, cs := range configured {
			if cs.Name == n {
				spec.Options = cs.Options
				break
			}
		}
		out = append(out, spec)
	}
	return out, nil
}

// Emit builds every sink and emits to it, stopping at the first error.
func Emit(ctx context.Context, specs []sink.Spec, env sink.Env, results []facts.ScanResult, m metrics.Metrics) error {
	for _, s := range specs {
		k, err := sink.New(s, env)
		if err != nil {
			return err
		}
		if err := k.Emit(ctx, results, m); err != nil {
			return fmt.Errorf("sink %s: %w", s.Name, err)
		}
	}
	return nil
}

// RemoteURL turns a scan argument that is not a local path into a clone URL:
// URLs and scp-style addresses pass through, and owner/name becomes a GitHub
// HTTPS URL under base (default https://github.com).
func RemoteURL(arg, base string) (string, error) {
	if strings.Contains(arg, "://") || strings.HasPrefix(arg, "git@") {
		return arg, nil
	}
	parts := strings.Split(arg, "/")
	if len(parts) == 2 && parts[0] != "" && parts[1] != "" && !strings.HasPrefix(arg, ".") {
		if base == "" {
			base = "https://github.com"
		}
		return strings.TrimSuffix(base, "/") + "/" + arg + ".git", nil
	}
	return "", fmt.Errorf("%s: not a directory, URL or owner/name", arg)
}
