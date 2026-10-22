package cmd

import (
	"fmt"
	"sync"

	"github.com/spf13/cobra"

	"github.com/rossedman/tfmetrics/internal/app"
	"github.com/rossedman/tfmetrics/internal/facts"
	"github.com/rossedman/tfmetrics/internal/metrics"
	"github.com/rossedman/tfmetrics/internal/report"
	"github.com/rossedman/tfmetrics/internal/ui"
)

var explainCmd = &cobra.Command{
	Use:   "explain <facts file|dir>...",
	Short: "Print a human-readable report for each repo",
	Long: `Print a per-repo report from facts files: module inventory, hygiene score
and the checks behind it, remote module calls and their pinning, and parse
diagnostics.

For each pinned remote module call, the report shows the latest release and
how many releases newer than the version the call uses there are, as major,
minor and patch releases. That needs registry and git tag lookups; --offline
skips them.`,
	Example: `  tfmetrics explain facts/acme__infra.json
  tfmetrics explain facts/ --format markdown
  tfmetrics explain facts/ --offline`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := app.LoadConfig(v)
		if err != nil {
			return err
		}
		results, err := facts.ReadAll(args)
		if err != nil {
			return err
		}
		format, _ := cmd.Flags().GetString("format")
		if format != "text" && format != "markdown" {
			return fmt.Errorf("unknown format %q (want text or markdown)", format)
		}
		gaps := make([]report.Gaps, len(results))
		if offline, _ := cmd.Flags().GetBool("offline"); !offline {
			gaps = lookUpGaps(cmd, results, newLister(cfg))
		}
		w := cmd.OutOrStdout()
		pretty := !plain && interactive(w)
		for i, r := range results {
			if i > 0 && format == "text" {
				fmt.Fprintln(w)
			}
			o := report.Options{Weights: cfg.Hygiene.Weights, Gaps: gaps[i]}
			switch {
			case format == "markdown":
				err = report.Markdown(w, r, o)
			case pretty:
				o.Width = ui.Width(w)
				err = report.Pretty(w, r, o)
			default:
				err = report.Text(w, r, o)
			}
			if err != nil {
				return err
			}
		}
		return nil
	},
}

// lookUpGaps looks up how far behind each repo's module calls are, showing
// progress and each failed lookup once on stderr.
func lookUpGaps(cmd *cobra.Command, results []facts.ScanResult, lister metrics.VersionLister) []report.Gaps {
	tr := ui.NewTracker(cmd.ErrOrStderr(), ui.Options{Title: "Looking up module versions", Noun: "modules", Live: live(cmd)})
	defer tr.Close()
	var once sync.Once
	return app.Gaps(cmd.Context(), results, lister, app.Hooks{
		Lookup: func(done, total int) {
			once.Do(func() { tr.SetTotal(total) })
			if done > 0 {
				tr.Done(ui.Event{})
			}
		},
		Warn: tr.Warn,
	})
}

// init registers the explain command and its flags.
func init() {
	explainCmd.Flags().String("format", "text", "output format: text or markdown")
	explainCmd.Flags().Bool("offline", false, "skip registry and git tag lookups (no latest or behind columns)")
	rootCmd.AddCommand(explainCmd)
}
