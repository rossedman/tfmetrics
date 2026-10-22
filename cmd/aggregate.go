package cmd

import (
	"github.com/spf13/cobra"

	"github.com/rossedman/tfmetrics/internal/app"
	"github.com/rossedman/tfmetrics/internal/facts"
	"github.com/rossedman/tfmetrics/internal/metrics"
	"github.com/rossedman/tfmetrics/internal/sink"
)

var aggregateCmd = &cobra.Command{
	Use:   "aggregate <facts dir|file>...",
	Short: "Compute the metrics catalog from facts files and write it to sinks",
	Long: `Read facts files, compute the metrics catalog and write it to every sink.
Sinks come from --sink, or from the sinks list in the config when no --sink
is given; with neither, the json sink is used. A --sink named in the config
takes its options from there.

Version drift (tf_module_versions_behind, tf_provider_versions_behind and
tf_terraform_versions_behind) needs registry and git tag lookups; --offline
skips them.`,
	Example: `  tfmetrics aggregate facts/ --sink openmetrics --sink parquet
  tfmetrics aggregate facts/ --sink json --out metrics/ --offline`,
	Args: cobra.MinimumNArgs(1),
	PreRunE: func(cmd *cobra.Command, _ []string) error {
		return bindFlags(cmd, map[string]string{"out": "out"})
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		cfg, err := app.LoadConfig(v)
		if err != nil {
			return err
		}
		results, err := facts.ReadAll(args)
		if err != nil {
			return err
		}
		specs, err := app.SinkSpecs(cfg, stringSlice(cmd.Flags(), "sink"))
		if err != nil {
			return err
		}
		if len(specs) == 0 {
			specs = []sink.Spec{{Name: "json"}}
		}
		var lister metrics.VersionLister
		if offline, _ := cmd.Flags().GetBool("offline"); !offline {
			lister = newLister(cfg)
		}
		m := aggregate(cmd, cfg, results, lister)
		out := v.GetString("out")
		if err := app.Emit(ctx, specs, sink.Env{OutDir: out, Weights: cfg.Hygiene.Weights, Stdout: cmd.OutOrStdout()}, results, m); err != nil {
			return err
		}
		return printAggregated(cmd, live(cmd), len(results), m, specs)
	},
}

// init registers the aggregate command and its flags.
func init() {
	aggregateCmd.Flags().StringSlice("sink", nil, "sinks to write: "+joinNames())
	aggregateCmd.Flags().String("out", "metrics", "default directory for file sinks")
	aggregateCmd.Flags().Bool("offline", false, "skip registry and git tag lookups (no versions_behind)")
	rootCmd.AddCommand(aggregateCmd)
}
