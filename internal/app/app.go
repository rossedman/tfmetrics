// Package app wires configuration into the pipeline stages, so cobra commands
// stay thin.
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/spf13/viper"

	"github.com/rossedman/tfmetrics/internal/config"
	"github.com/rossedman/tfmetrics/internal/discover"
	"github.com/rossedman/tfmetrics/internal/facts"
	"github.com/rossedman/tfmetrics/internal/metrics"
	"github.com/rossedman/tfmetrics/internal/pipeline"
	"github.com/rossedman/tfmetrics/internal/registry"
	"github.com/rossedman/tfmetrics/internal/report"
	"github.com/rossedman/tfmetrics/internal/scan"
	"github.com/rossedman/tfmetrics/internal/sink"
	"github.com/rossedman/tfmetrics/internal/source"
)

// KeyDelimiter separates nested config keys in the viper instance from
// NewViper. Module alias keys are module sources, which contain dots
// (github.com/acme/old) and "::" (git::https://...), so the delimiter is a
// control character that cannot appear in one.
const KeyDelimiter = "\x1f"

// NewViper returns a viper instance for tfmetrics config: nested keys are
// joined with KeyDelimiter and environment variables are TFMETRICS_ plus the
// key with delimiters and dashes as underscores.
func NewViper() *viper.Viper {
	v := viper.NewWithOptions(viper.KeyDelimiter(KeyDelimiter))
	v.SetEnvPrefix("TFMETRICS")
	v.SetEnvKeyReplacer(strings.NewReplacer(KeyDelimiter, "_", "-", "_"))
	v.AutomaticEnv()
	return v
}

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
	return source.NewNormalizer(registryHosts(c), c.Modules.Aliases)
}

// registryHosts returns the hosts of every configured private registry.
func registryHosts(c config.Config) []string {
	var hosts []string
	for _, r := range c.Modules.AllRegistries() {
		hosts = append(hosts, r.Host)
	}
	return hosts
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
		Reg     []string
		Aliases map[string]string
		Owner   string
	}{facts.SchemaVersion, buildVersion, c.Scan.IgnoreDirs, c.Scan.RootOverrides, registryHosts(c), c.Modules.Aliases, c.Owners.Property})
	return string(b)
}

// GitHubToken returns the GitHub token from the configured environment variable.
func GitHubToken(c config.Config) string {
	return os.Getenv(c.Source.GitHub.TokenEnv)
}

// Resolver builds a version resolver that authenticates to each private
// registry with its own token and to GitHub for git tag lookups. Registries
// without a token_env fall back to Terraform's TF_TOKEN_<host> variables.
func Resolver(c config.Config) *registry.Resolver {
	r := registry.New()
	r.Tokens = map[string]string{}
	for _, reg := range c.Modules.AllRegistries() {
		if reg.TokenEnv != "" {
			if t := os.Getenv(reg.TokenEnv); t != "" {
				r.Tokens[reg.Host] = t
			}
		}
	}
	if t := GitHubToken(c); t != "" {
		r.GitToken, r.GitTokenHost = t, "github.com"
	}
	return r
}

// Hooks report on Aggregate as it runs; nil fields are skipped.
type Hooks struct {
	// Lookup is called with (0, total) before version lookups start and after
	// each one finishes, from any goroutine.
	Lookup func(done, total int)
	// Warn reports a failed version lookup.
	Warn func(msg string)
}

// Aggregate computes metrics. With lister nil, versions_behind is skipped.
func Aggregate(ctx context.Context, c config.Config, results []facts.ScanResult, lister metrics.VersionLister, hooks Hooks) metrics.Metrics {
	opts := metrics.Options{
		PropertyLabels: c.Labels.Properties,
		DropLabels:     c.Labels.Drop,
		Weights:        c.Hygiene.Weights,
	}
	if lister != nil {
		metrics.Prefetch(ctx, results, lister, registry.LookupConcurrency, hooks.Lookup)
		opts.Versions = lister
		opts.OnLookupError = func(addr string, err error) {
			if hooks.Warn != nil {
				hooks.Warn(fmt.Sprintf("version lookup for %s: %v", addr, err))
			}
		}
	}
	return metrics.Compute(ctx, results, opts)
}

// Gaps looks up how far behind the latest release each remote module call in
// results is, for explain: one report.Gaps per result, in order. A failed
// lookup is reported once per address through hooks.Warn, and its calls are
// left out.
func Gaps(ctx context.Context, results []facts.ScanResult, lister metrics.VersionLister, hooks Hooks) []report.Gaps {
	metrics.PrefetchModules(ctx, results, lister, registry.LookupConcurrency, hooks.Lookup)
	failed := map[string]bool{}
	onErr := func(addr string, err error) {
		if !failed[addr] && hooks.Warn != nil {
			hooks.Warn(fmt.Sprintf("version lookup for %s: %v", addr, err))
		}
		failed[addr] = true
	}
	out := make([]report.Gaps, len(results))
	for i, r := range results {
		out[i] = report.Gaps{}
		for _, m := range r.Modules {
			for _, c := range m.ModuleCalls {
				if g, ok := metrics.CallGap(ctx, c, lister, onErr); ok {
					out[i][report.GapKey(m.Path, c.Name)] = g
				}
			}
		}
	}
	return out
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

// RunStats summarizes where an org run spent its time and, when discovery
// used the GitHub API, how much of the rate limit it used.
func RunStats(sum pipeline.Summary, gh *discover.GitHub) string {
	var b strings.Builder
	fmt.Fprintf(&b, "time: %s wall; summed across workers: ls-remote %s (%d), clone %s (%d), parse %s",
		sum.Wall.Round(time.Millisecond), sum.HeadTime.Round(time.Millisecond), sum.RemoteHeads,
		sum.CloneTime.Round(time.Millisecond), sum.Clones, sum.ScanTime.Round(time.Millisecond))
	if gh == nil || gh.Transport == nil {
		return b.String()
	}
	st := gh.Transport.Stats()
	fmt.Fprintf(&b, "\ngithub api: %d requests, %d retries, waited %s", st.Requests, st.Retries, st.Waited.Round(time.Second))
	names := make([]string, 0, len(st.Limits))
	for k := range st.Limits {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		l := st.Limits[k]
		fmt.Fprintf(&b, "; %s %d/%d left", k, l.Remaining, l.Limit)
	}
	return b.String()
}

// ConfigNames are the file names the default config search accepts. Only
// these: a bare "tfmetrics" would match the binary itself when it is run from
// the directory it was unpacked into.
var ConfigNames = []string{"tfmetrics.yaml", "tfmetrics.yml"}

// FindConfig returns the first regular file named in ConfigNames in dirs,
// searched in order, or "" when there is none.
func FindConfig(dirs []string) string {
	for _, d := range dirs {
		for _, n := range ConfigNames {
			p := filepath.Join(d, n)
			if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() {
				return p
			}
		}
	}
	return ""
}

// DefaultGCPercent is the garbage collector target unless GOGC is set.
// Parsing allocates heavily but keeps little alive, so collecting half as
// often scans about 17% faster for about a third more peak memory.
const DefaultGCPercent = 200

// TuneGC applies DefaultGCPercent unless getenv("GOGC") is set.
func TuneGC(getenv func(string) string) {
	if getenv("GOGC") == "" {
		debug.SetGCPercent(DefaultGCPercent)
	}
}
