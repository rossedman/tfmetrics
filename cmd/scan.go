package cmd

import (
	"fmt"
	"os"
	"sync"

	"github.com/spf13/cobra"

	"github.com/rossedman/tfmetrics/internal/app"
	"github.com/rossedman/tfmetrics/internal/clone"
	"github.com/rossedman/tfmetrics/internal/config"
	"github.com/rossedman/tfmetrics/internal/facts"
	"github.com/rossedman/tfmetrics/internal/scan"
	"github.com/rossedman/tfmetrics/internal/sink"
)

var scanCmd = &cobra.Command{
	Use:   "scan <path|repo>...",
	Short: "Parse repos into facts JSON",
	Long: `Parse one or more repos into facts files, one per repo. Each argument is a
local directory, a git URL, or a GitHub owner/name (shallow-cloned with a
sparse checkout). Nothing is fetched beyond the clone.

With --sink, the facts are also aggregated and emitted, which is how the
per-repo PR check works: tfmetrics scan . --sink markdown

A repo that cannot be cloned or scanned is reported and skipped; the others
are still written and emitted, and the command exits non-zero.`,
	Example: `  tfmetrics scan .
  tfmetrics scan ./infra acme/tf-network --out facts/
  tfmetrics scan . --sink markdown`,
	Args: cobra.MinimumNArgs(1),
	PreRunE: func(cmd *cobra.Command, _ []string) error {
		return bindFlags(cmd, map[string]string{
			"out":         "out",
			"ref":         "scan" + app.KeyDelimiter + "ref",
			"concurrency": "scan" + app.KeyDelimiter + "concurrency",
		})
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		cfg, err := app.LoadConfig(v)
		if err != nil {
			return err
		}
		out := v.GetString("out")
		opts := app.ScanOptions(cfg)
		git := clone.Git{Token: app.GitHubToken(cfg)}
		// Clone and scan up to scan.concurrency repos at once; clones spend
		// most of their time waiting on the network. Each repo is written and
		// reported as it finishes; the sinks see the repos in argument order.
		scanned := make([]*facts.ScanResult, len(args))
		var mu sync.Mutex
		var failed int
		var writeErr error
		sem := make(chan struct{}, max(cfg.Scan.Concurrency, 1))
		var wg sync.WaitGroup
		for i, arg := range args {
			wg.Go(func() {
				sem <- struct{}{}
				defer func() { <-sem }()
				res, err := scanArg(cmd, arg, git, opts, cfg)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					// One unreachable repo must not cost the others their scan.
					failed++
					fmt.Fprintf(cmd.ErrOrStderr(), "error: %v\n", err)
					return
				}
				path, err := facts.Write(out, res)
				if err != nil {
					if writeErr == nil {
						writeErr = err
					}
					return
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "scanned %s: %d modules -> %s\n", res.Repo, len(res.Modules), path)
				scanned[i] = &res
			})
		}
		wg.Wait()
		if writeErr != nil {
			return writeErr
		}
		var results []facts.ScanResult
		for _, r := range scanned {
			if r != nil {
				results = append(results, *r)
			}
		}
		var scanErr error
		if failed > 0 {
			scanErr = fmt.Errorf("%d of %d repos failed to scan", failed, len(args))
		}
		names := stringSlice(cmd.Flags(), "sink")
		if len(names) == 0 || len(results) == 0 {
			return scanErr
		}
		specs, err := app.SinkSpecs(cfg, names)
		if err != nil {
			return err
		}
		m := app.Aggregate(ctx, cfg, results, nil, cmd.ErrOrStderr())
		if err := app.Emit(ctx, specs, sink.Env{OutDir: out, Weights: cfg.Hygiene.Weights, Stdout: cmd.OutOrStdout()}, results, m); err != nil {
			return err
		}
		return scanErr
	},
}

// scanArg scans a local directory, or clones and scans a remote repo.
func scanArg(cmd *cobra.Command, arg string, git clone.Git, opts scan.Options, cfg config.Config) (facts.ScanResult, error) {
	if info, err := os.Stat(arg); err == nil && info.IsDir() {
		res, err := scan.Dir(cmd.Context(), arg, scan.Meta{}, opts)
		if err != nil {
			return res, fmt.Errorf("%s: %w", arg, err)
		}
		return res, nil
	}
	return scanRemote(cmd, arg, git, opts, cfg.Source.GitHub.CloneURL, cfg.Scan.Ref)
}

// scanRemote clones a repo into a temporary directory and scans it.
func scanRemote(cmd *cobra.Command, arg string, git clone.Git, opts scan.Options, base, ref string) (facts.ScanResult, error) {
	url, err := app.RemoteURL(arg, base)
	if err != nil {
		return facts.ScanResult{}, err
	}
	tmp, err := os.MkdirTemp("", "tfmetrics-scan-")
	if err != nil {
		return facts.ScanResult{}, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	sha, err := git.Clone(cmd.Context(), url, ref, tmp)
	if err != nil {
		return facts.ScanResult{}, fmt.Errorf("%s: %w", arg, err)
	}
	res, err := scan.Dir(cmd.Context(), tmp, scan.Meta{Repo: scan.RepoFromURL(url), Commit: sha}, opts)
	if err != nil {
		return facts.ScanResult{}, fmt.Errorf("%s: %w", arg, err)
	}
	return res, nil
}

// init registers the scan command and its flags.
func init() {
	scanCmd.Flags().String("out", "facts", "directory to write facts files to")
	scanCmd.Flags().String("ref", "", "branch, tag or commit to clone for remote repos (default: the default branch)")
	scanCmd.Flags().StringSlice("sink", nil, "also aggregate and emit to these sinks: "+joinNames())
	scanCmd.Flags().Int("concurrency", 16, "repos to clone and scan at once")
	rootCmd.AddCommand(scanCmd)
}
