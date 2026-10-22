package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/rossedman/tfmetrics/internal/app"
	"github.com/rossedman/tfmetrics/internal/cache"
	"github.com/rossedman/tfmetrics/internal/clone"
	"github.com/rossedman/tfmetrics/internal/config"
	"github.com/rossedman/tfmetrics/internal/discover"
	"github.com/rossedman/tfmetrics/internal/facts"
	"github.com/rossedman/tfmetrics/internal/filter"
	"github.com/rossedman/tfmetrics/internal/pipeline"
	"github.com/rossedman/tfmetrics/internal/sink"
	"github.com/rossedman/tfmetrics/internal/ui"
)

var orgCmd = &cobra.Command{
	Use:   "org",
	Short: "Discover, clone and scan every matching repo in one or more orgs",
	Long: `Discover repos in one or more GitHub orgs, apply each org's name, topic and
custom property filters, then shallow-clone and scan the matches in parallel.
A repo whose HEAD matches the cache reuses its last facts without cloning.

Orgs and filters come from source.github.orgs in the config. --org replaces
that list for a one-off run; filter flags replace the include filters of
every org named. --local-dir scans a directory of existing clones instead of
calling GitHub.`,
	Example: `  tfmetrics org --out facts/
  tfmetrics org --org acme --org acme-labs --out facts/
  tfmetrics org --org acme --topic terraform --property team=platform,networking --name 'tf-*' --dry-run
  tfmetrics org --org acme --local-dir ./clones --out facts/`,
	Args: cobra.NoArgs,
	PreRunE: func(cmd *cobra.Command, _ []string) error {
		return bindFlags(cmd, map[string]string{
			"out":         "out",
			"ref":         "scan" + app.KeyDelimiter + "ref",
			"concurrency": "scan" + app.KeyDelimiter + "concurrency",
			"cache-dir":   "scan" + app.KeyDelimiter + "cache_dir",
		})
	},
	RunE: func(cmd *cobra.Command, _ []string) error {
		ctx := cmd.Context()
		cfg, err := app.LoadConfig(v)
		if err != nil {
			return err
		}
		orgs, err := orgsFromFlags(cmd, cfg.Source.GitHub.Orgs)
		if err != nil {
			return err
		}
		if len(orgs) == 0 {
			return errors.New("no orgs: set source.github.orgs in the config or pass --org")
		}
		token := app.GitHubToken(cfg)
		var repos []discover.Repo
		var gh *discover.GitHub
		localDir, _ := cmd.Flags().GetString("local-dir")
		if localDir != "" {
			if len(orgs) != 1 {
				return errors.New("--local-dir takes exactly one --org to name the repos")
			}
			repos, err = discover.LocalDir(localDir, orgs[0].Name, orgs[0].Include, orgs[0].Exclude)
		} else {
			repos, gh, err = discoverGitHub(cmd, cfg, orgs, token)
		}
		if err != nil {
			return err
		}

		if dry, _ := cmd.Flags().GetBool("dry-run"); dry {
			return printRepos(cmd, repos)
		}

		out := v.GetString("out")
		c := cache.Cache{Dir: cfg.Scan.CacheDir}
		if noCache, _ := cmd.Flags().GetBool("no-cache"); noCache {
			c.Dir = ""
		}
		stderr := cmd.ErrOrStderr()
		lv := live(cmd)
		start := time.Now()
		tr := ui.NewTracker(stderr, ui.Options{
			Title: fmt.Sprintf("Scanning %s %s in %s", ui.Count(len(repos)), plural(len(repos), "repo"), orgNames(orgs)),
			Total: len(repos), Noun: "repos", Live: lv,
		})
		sum := pipeline.Run(ctx, repos, pipeline.Options{
			OutDir:      out,
			Concurrency: cfg.Scan.Concurrency,
			Ref:         cfg.Scan.Ref,
			Cache:       c,
			Cloner:      clone.Git{Token: token},
			Scan:        app.ScanOptions(cfg),
			Settings:    app.Settings(cfg, version),
			Progress: func(repo string, cached bool, err error) {
				switch {
				case err != nil && ctx.Err() != nil:
					// cut short by the interrupt, not a failure of its own
				case err != nil:
					tr.Done(ui.Event{Name: repo, Status: ui.Failed, Err: err, Plain: fmt.Sprintf("error   %s: %v", repo, err)})
				case cached:
					tr.Done(ui.Event{Name: repo, Status: ui.Cached, Plain: "cached  " + repo})
				default:
					tr.Done(ui.Event{Name: repo, Plain: "scanned " + repo})
				}
			},
		})
		tr.Close()
		if err := ctx.Err(); err != nil {
			finished := sum.Scanned + sum.Cached
			_ = ui.Line(stderr, lv,
				ui.Warn.Render(fmt.Sprintf("%s Interrupted after %s of %s repos", ui.IconWarn, ui.Count(finished), ui.Count(len(repos)))),
				fmt.Sprintf("interrupted after %d of %d repos", finished, len(repos)))
			return err
		}
		if err := printRunSummary(stderr, lv, sum, gh, len(repos), out, time.Since(start)); err != nil {
			return err
		}
		if len(sum.Failed) > 0 && len(sum.Failed) == len(repos) {
			return fmt.Errorf("every repo failed to scan")
		}
		if strict, _ := cmd.Flags().GetBool("strict"); strict && len(sum.Failed) > 0 {
			return fmt.Errorf("%d repos failed to scan", len(sum.Failed))
		}

		names := stringSlice(cmd.Flags(), "sink")
		if len(names) == 0 {
			return nil
		}
		results, err := facts.ReadAll([]string{out})
		if err != nil {
			return err
		}
		specs, err := app.SinkSpecs(cfg, names)
		if err != nil {
			return err
		}
		m := aggregate(cmd, cfg, results, app.Resolver(cfg))
		if err := app.Emit(ctx, specs, sink.Env{OutDir: out, Weights: cfg.Hygiene.Weights, Stdout: cmd.OutOrStdout()}, results, m); err != nil {
			return err
		}
		if !lv {
			return nil // the plain run summary above already said where the facts went
		}
		return printAggregated(cmd, true, len(results), m, specs)
	},
}

// discoverGitHub lists the matching repos in each org, waiting out rate
// limits and reporting each wait on stderr.
func discoverGitHub(cmd *cobra.Command, cfg config.Config, orgs []config.Org, token string) ([]discover.Repo, *discover.GitHub, error) {
	gh, err := discover.NewGitHub(token, cfg.Source.GitHub.BaseURL, 10)
	if err != nil {
		return nil, nil, err
	}
	lv := live(cmd)
	start := time.Now()
	tr := ui.NewTracker(cmd.ErrOrStderr(), ui.Options{Title: "Discovering repos in " + orgNames(orgs), Live: lv})
	gh.Transport.MaxRetries = cfg.Source.GitHub.MaxRetries
	gh.Transport.OnWait = func(reason string, d time.Duration) { tr.Wait("github api: "+reason, d) }
	gh.OnWarning = tr.Warn
	var repos []discover.Repo
	for _, o := range orgs {
		found, err := gh.Org(cmd.Context(), o, cfg.Source.GitHub.Defaults)
		if err != nil {
			tr.Close()
			return nil, nil, err
		}
		repos = append(repos, found...)
	}
	tr.Close()
	if lv {
		err = ui.Line(cmd.ErrOrStderr(), true, fmt.Sprintf("%s Found %s %s in %s %s",
			ui.OK.Render(ui.IconOK), ui.Bold.Render(ui.Count(len(repos))), plural(len(repos), "repo"),
			orgNames(orgs), ui.Subtle.Render("in "+ui.Duration(time.Since(start)))), "")
	}
	return repos, gh, err
}

// orgNames lists the orgs being scanned: "acme, acme-labs".
func orgNames(orgs []config.Org) string {
	names := make([]string, len(orgs))
	for i, o := range orgs {
		names[i] = o.Name
	}
	return strings.Join(names, ", ")
}

// printRunSummary reports how a scan of total repos went: a styled summary on
// a live terminal, the plain counts and timings otherwise.
func printRunSummary(w io.Writer, lv bool, sum pipeline.Summary, gh *discover.GitHub, total int, out string, took time.Duration) error {
	stats := app.RunStats(sum, gh)
	if !lv {
		_, err := fmt.Fprintf(w, "%d repos: %d scanned, %d from cache, %d failed -> %s\n%s\n",
			total, sum.Scanned, sum.Cached, len(sum.Failed), out, stats)
		return err
	}
	icon, verb := ui.OK.Render(ui.IconOK), "Scanned"
	if len(sum.Failed) > 0 {
		icon = ui.Warn.Render(ui.IconWarn)
	}
	if len(sum.Failed) == total && total > 0 {
		icon, verb = ui.Err.Render(ui.IconErr), "Failed to scan"
	}
	counts := []string{
		ui.OK.Render(fmt.Sprintf("%s scanned", ui.Count(sum.Scanned))),
		ui.Subtle.Render(fmt.Sprintf("%s from cache", ui.Count(sum.Cached))),
	}
	if len(sum.Failed) > 0 {
		counts = append(counts, ui.Err.Render(fmt.Sprintf("%s failed", ui.Count(len(sum.Failed)))))
	}
	lines := []string{
		fmt.Sprintf("%s %s %s %s in %s %s", icon, verb, ui.Bold.Render(ui.Count(total)), plural(total, "repo"), ui.Duration(took), ui.Subtle.Render("→ "+out)),
		"  " + strings.Join(counts, ui.Subtle.Render(" · ")),
	}
	for _, l := range strings.Split(stats, "\n") {
		lines = append(lines, "  "+ui.Subtle.Render(l))
	}
	return ui.Line(w, true, strings.Join(lines, "\n"), "")
}

// orgsFromFlags applies --org and the filter flags to the configured orgs.
func orgsFromFlags(cmd *cobra.Command, configured []config.Org) ([]config.Org, error) {
	names := stringSlice(cmd.Flags(), "org")
	inc := config.Filter{
		Names:  stringSlice(cmd.Flags(), "name"),
		Topics: stringSlice(cmd.Flags(), "topic"),
	}
	props, err := cmd.Flags().GetStringArray("property")
	if err != nil {
		return nil, err
	}
	for _, p := range props {
		k, vals, err := filter.ParseProperty(p)
		if err != nil {
			return nil, err
		}
		if inc.Properties == nil {
			inc.Properties = map[string][]string{}
		}
		inc.Properties[k] = append(inc.Properties[k], vals...)
	}
	orgs := configured
	if len(names) > 0 {
		byName := map[string]config.Org{}
		for _, o := range configured {
			byName[o.Name] = o
		}
		orgs = nil
		for _, n := range names {
			o, ok := byName[n]
			if !ok {
				o = config.Org{Name: n}
			}
			orgs = append(orgs, o)
		}
	}
	if !inc.Empty() {
		for i := range orgs {
			orgs[i].Include = inc
		}
	}
	return orgs, nil
}

// printRepos prints the matched repos for --dry-run, as text or JSON.
func printRepos(cmd *cobra.Command, repos []discover.Repo) error {
	w := cmd.OutOrStdout()
	if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
		if repos == nil {
			repos = []discover.Repo{}
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(repos)
	}
	for _, r := range repos {
		var props []string
		for k, val := range r.Properties {
			props = append(props, k+"="+val)
		}
		sort.Strings(props)
		fmt.Fprintf(w, "%s\ttopics=%s\t%s\n", r.FullName, strings.Join(r.Topics, ","), strings.Join(props, " "))
	}
	_, err := fmt.Fprintf(w, "%d repos matched\n", len(repos))
	return err
}

// init registers the org command and its flags.
func init() {
	f := orgCmd.Flags()
	f.StringSlice("org", nil, "GitHub org to scan; repeat for several (replaces source.github.orgs)")
	f.StringSlice("name", nil, "include repos whose name matches a glob, or a regex with a re: prefix")
	f.StringSlice("topic", nil, "include repos with any of these topics")
	f.StringArray("property", nil, "include repos whose custom property matches: key=value[,value]; repeat for more keys")
	f.Bool("dry-run", false, "print the matched repos without cloning")
	f.Bool("json", false, "with --dry-run, print the matched repos as JSON")
	f.String("out", "facts", "directory to write facts files to")
	f.String("ref", "", "branch, tag or commit to scan (default: each repo's default branch)")
	f.Int("concurrency", 16, "repos to clone and scan at once")
	f.String("cache-dir", "", "facts cache keyed by repo and commit (default: the user cache dir)")
	f.Bool("no-cache", false, "ignore the cache and rescan every repo")
	f.String("local-dir", "", "scan the git checkouts in this directory instead of calling GitHub")
	f.Bool("strict", false, "exit non-zero if any repo fails to scan")
	f.StringSlice("sink", nil, "also aggregate and emit to these sinks: "+joinNames())
	rootCmd.AddCommand(orgCmd)
}
