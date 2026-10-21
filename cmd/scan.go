package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/rossedman/tfmetrics/internal/app"
	"github.com/rossedman/tfmetrics/internal/clone"
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
per-repo PR check works: tfmetrics scan . --sink markdown`,
	Example: `  tfmetrics scan .
  tfmetrics scan ./infra acme/tf-network --out facts/
  tfmetrics scan . --sink markdown`,
	Args: cobra.MinimumNArgs(1),
	PreRunE: func(cmd *cobra.Command, _ []string) error {
		return bindFlags(cmd, map[string]string{"out": "out", "ref": "scan.ref"})
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
		var results []facts.ScanResult
		for _, arg := range args {
			var res facts.ScanResult
			if info, statErr := os.Stat(arg); statErr == nil && info.IsDir() {
				res, err = scan.Dir(ctx, arg, scan.Meta{}, opts)
			} else {
				res, err = scanRemote(cmd, arg, git, opts, cfg.Source.GitHub.CloneURL, cfg.Scan.Ref)
			}
			if err != nil {
				return err
			}
			path, err := facts.Write(out, res)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "scanned %s: %d modules -> %s\n", res.Repo, len(res.Modules), path)
			results = append(results, res)
		}
		names := stringSlice(cmd.Flags(), "sink")
		if len(names) == 0 {
			return nil
		}
		specs, err := app.SinkSpecs(cfg, names)
		if err != nil {
			return err
		}
		m := app.Aggregate(ctx, cfg, results, nil, cmd.ErrOrStderr())
		return app.Emit(ctx, specs, sink.Env{OutDir: out, Weights: cfg.Hygiene.Weights, Stdout: cmd.OutOrStdout()}, results, m)
	},
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
		return facts.ScanResult{}, err
	}
	return scan.Dir(cmd.Context(), tmp, scan.Meta{Repo: scan.RepoFromURL(url), Commit: sha}, opts)
}

// init registers the scan command and its flags.
func init() {
	scanCmd.Flags().String("out", "facts", "directory to write facts files to")
	scanCmd.Flags().String("ref", "", "branch, tag or commit to clone for remote repos (default: the default branch)")
	scanCmd.Flags().StringSlice("sink", nil, "also aggregate and emit to these sinks: "+joinNames())
	rootCmd.AddCommand(scanCmd)
}
