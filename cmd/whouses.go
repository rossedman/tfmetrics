package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/rossedman/tfmetrics/internal/app"
	"github.com/rossedman/tfmetrics/internal/facts"
	"github.com/rossedman/tfmetrics/internal/metrics"
	"github.com/rossedman/tfmetrics/internal/report"
	"github.com/rossedman/tfmetrics/internal/ui"
	"github.com/rossedman/tfmetrics/internal/usage"
)

var whoUsesCmd = &cobra.Command{
	Use:   "who-uses <module|provider> <facts dir|file>...",
	Short: "List every call to a module, or use of a provider, across facts files",
	Long: `List every call to a module across facts files: which repo and module makes
it, where the module block is, the version it asks for, how many release
lines that version is behind, and who owns the repo. With --provider, list
every required_providers entry for a provider instead.

The module or provider can be written whole or by its trailing segments:
terraform-aws-modules/eks/aws and eks/aws both match
registry.terraform.io/terraform-aws-modules/eks/aws, including calls to its
//subdirectories. A provider is matched by its source, such as hashicorp/aws.

How far behind each use is needs registry and git tag lookups; --offline skips
them. --behind keeps only the uses at least that far behind, which makes the
list an upgrade campaign: every caller still to move, and its owner.`,
	Example: `  tfmetrics who-uses terraform-aws-modules/eks/aws facts/
  tfmetrics who-uses eks/aws facts/ --behind 2
  tfmetrics who-uses hashicorp/aws facts/ --provider --format csv > aws.csv`,
	Args: cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := app.LoadConfig(v)
		if err != nil {
			return err
		}
		format, _ := cmd.Flags().GetString("format")
		if format != "text" && format != "json" && format != "csv" {
			return fmt.Errorf("unknown format %q (want text, json or csv)", format)
		}
		provider, _ := cmd.Flags().GetBool("provider")
		offline, _ := cmd.Flags().GetBool("offline")
		minBehind, _ := cmd.Flags().GetInt("behind")
		if minBehind > 0 && offline {
			return errors.New("--behind needs version lookups; drop --offline")
		}
		results, err := facts.ReadAll(args[1:])
		if err != nil {
			return err
		}
		q := usage.Query{Target: args[0], Provider: provider}
		uses := usage.Find(results, q)
		if !offline && len(uses) > 0 {
			lookUpBehind(cmd, uses, provider, newLister(cfg))
		}
		if minBehind > 0 {
			uses = usage.AtLeastBehind(uses, minBehind)
		}
		return writeUses(cmd.OutOrStdout(), uses, format, report.UsageOptions{Target: args[0], Provider: provider, Behind: !offline})
	},
}

// lookUpBehind fills in how far behind each use is, showing progress and
// each failed lookup once on stderr.
func lookUpBehind(cmd *cobra.Command, uses []usage.Use, provider bool, lister metrics.VersionLister) {
	tr := ui.NewTracker(cmd.ErrOrStderr(), ui.Options{Title: "Looking up versions", Total: len(uses), Noun: "uses", Live: live(cmd)})
	defer tr.Close()
	failed := map[string]bool{}
	onErr := func(addr string, err error) {
		if !failed[addr] {
			failed[addr] = true
			tr.Warn(fmt.Sprintf("version lookup for %s: %v", addr, err))
		}
	}
	for i := range uses {
		usage.Behind(cmd.Context(), uses[i:i+1], provider, lister, onErr)
		tr.Done(ui.Event{Name: uses[i].Repo})
	}
}

// writeUses writes uses to w in format: a table (styled on a terminal), JSON
// or CSV.
func writeUses(w io.Writer, uses []usage.Use, format string, o report.UsageOptions) error {
	switch format {
	case "json":
		if uses == nil {
			uses = []usage.Use{}
		}
		b, err := json.MarshalIndent(uses, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(w, "%s\n", b)
		return err
	case "csv":
		return report.UsageCSV(w, uses)
	}
	if !plain && interactive(w) {
		o.Width = ui.Width(w)
		return report.PrettyUsage(w, uses, o)
	}
	return report.Usage(w, uses, o)
}

// init registers the who-uses command and its flags.
func init() {
	whoUsesCmd.Flags().Bool("provider", false, "search provider requirements instead of module calls")
	whoUsesCmd.Flags().Int("behind", 0, "only list uses at least this many release lines behind the latest")
	whoUsesCmd.Flags().Bool("offline", false, "skip registry and git tag lookups (no behind counts)")
	whoUsesCmd.Flags().String("format", "text", "output format: text, json or csv")
	rootCmd.AddCommand(whoUsesCmd)
}
