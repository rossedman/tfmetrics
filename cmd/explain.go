package cmd

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/rossedman/tfmetrics/internal/app"
	"github.com/rossedman/tfmetrics/internal/facts"
	"github.com/rossedman/tfmetrics/internal/report"
	"github.com/rossedman/tfmetrics/internal/ui"
)

var explainCmd = &cobra.Command{
	Use:   "explain <facts file|dir>...",
	Short: "Print a human-readable report for each repo",
	Long: `Print a per-repo report from facts files: module inventory, hygiene score
and the checks behind it, remote module calls and their pinning, and parse
diagnostics.`,
	Example: `  tfmetrics explain facts/acme__infra.json
  tfmetrics explain facts/ --format markdown`,
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
		w := cmd.OutOrStdout()
		text := report.Text
		if !plain && interactive(w) {
			width := ui.Width(w)
			text = func(w io.Writer, r facts.ScanResult, weights map[string]float64) error {
				return report.Pretty(w, r, weights, width)
			}
		}
		for i, r := range results {
			if i > 0 && format == "text" {
				fmt.Fprintln(w)
			}
			switch format {
			case "text":
				err = text(w, r, cfg.Hygiene.Weights)
			case "markdown":
				err = report.Markdown(w, r, cfg.Hygiene.Weights)
			default:
				return fmt.Errorf("unknown format %q (want text or markdown)", format)
			}
			if err != nil {
				return err
			}
		}
		return nil
	},
}

// init registers the explain command and its flags.
func init() {
	explainCmd.Flags().String("format", "text", "output format: text or markdown")
	rootCmd.AddCommand(explainCmd)
}
