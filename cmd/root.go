// Package cmd holds the tfmetrics cobra commands, one per file.
package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"

	"charm.land/lipgloss/v2"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/rossedman/tfmetrics/internal/app"
	"github.com/rossedman/tfmetrics/internal/config"
	"github.com/rossedman/tfmetrics/internal/facts"
	"github.com/rossedman/tfmetrics/internal/metrics"
	"github.com/rossedman/tfmetrics/internal/sink"
	"github.com/rossedman/tfmetrics/internal/ui"
)

var (
	cfgFile string
	plain   bool
	v       = app.NewViper()
	// interactive reports whether w gets live, styled output; tests replace it.
	interactive = ui.Interactive
)

var rootCmd = &cobra.Command{
	Use:   "tfmetrics",
	Short: "Org-wide Terraform metrics from static parsing",
	Long: `tfmetrics statically parses every Terraform repo in an org and emits one
consistent set of metrics: inventory, version drift, module adoption and
hygiene. It never runs terraform init or plan, and never touches state or
cloud credentials.

Config precedence: flag > environment (TFMETRICS_*) > config file.`,
	SilenceUsage:  true,
	SilenceErrors: true,
	PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
		return initConfig()
	},
}

// Execute runs the root command and exits non-zero on error. Ctrl-C or
// SIGTERM cancels the command's context, so work stops cleanly and the
// terminal is restored; a second Ctrl-C exits at once.
func Execute() {
	app.TuneGC(os.Getenv)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		stop()
	}()
	err := rootCmd.ExecuteContext(ctx)
	if err == nil {
		return
	}
	if ctx.Err() != nil && errors.Is(err, context.Canceled) {
		printError(os.Stderr, errors.New("interrupted"))
		os.Exit(130)
	}
	printError(os.Stderr, err)
	os.Exit(1)
}

// printError writes err to w, styled on a terminal.
func printError(w io.Writer, err error) {
	if !plain && interactive(w) {
		_, _ = lipgloss.Fprintln(w, ui.Err.Render(ui.IconErr+" error:"), err)
		return
	}
	fmt.Fprintln(w, "error:", err)
}

// live reports whether cmd shows live progress and styled output on stderr.
func live(cmd *cobra.Command) bool {
	return !plain && interactive(cmd.ErrOrStderr())
}

// init registers the persistent flags.
func init() {
	rootCmd.PersistentFlags().BoolVar(&plain, "plain", false, "print plain lines: no colors, styling or live progress, as when output is not a terminal")
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default: the first tfmetrics.yaml or tfmetrics.yml in $XDG_CONFIG_HOME/tfmetrics, ~/.config/tfmetrics, $HOME, then the current directory)")
}

// configDirs lists where tfmetrics.yaml is looked for, in order.
func configDirs() []string {
	var dirs []string
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		dirs = append(dirs, filepath.Join(x, "tfmetrics"))
	}
	if h, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(h, ".config", "tfmetrics"), h)
	}
	return append(dirs, ".")
}

// initConfig reads the config file and environment into v.
func initConfig() error {
	if cfgFile != "" {
		v.SetConfigFile(cfgFile)
		// .json and .toml keep their format; every other name (tfmetrics.conf,
		// /dev/stdin, a Viper-only format like .ini that cannot hold nested
		// config) is read as YAML.
		if ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(cfgFile), ".")); !slices.Contains([]string{"json", "toml"}, ext) {
			v.SetConfigType("yaml")
		}
	} else if p := app.FindConfig(configDirs()); p != "" {
		v.SetConfigFile(p)
		v.SetConfigType("yaml")
	}
	if cfgFile != "" || v.ConfigFileUsed() != "" {
		if err := v.ReadInConfig(); err != nil {
			return fmt.Errorf("reading config: %w", err)
		}
	}
	return nil
}

// bindFlags binds each named flag of cmd to a config key, so a set flag
// overrides the environment and the config file.
func bindFlags(cmd *cobra.Command, keys map[string]string) error {
	for flag, key := range keys {
		f := cmd.Flags().Lookup(flag)
		if f == nil {
			return fmt.Errorf("unknown flag %q", flag)
		}
		if err := v.BindPFlag(key, f); err != nil {
			return err
		}
	}
	return nil
}

// stringSlice returns a string slice flag's value.
func stringSlice(fs *pflag.FlagSet, name string) []string {
	s, _ := fs.GetStringSlice(name)
	return s
}

// joinNames lists the sink names for flag help.
func joinNames() string {
	return strings.Join(sink.Names, ", ")
}

// Root returns the root command, for tools that walk the command tree.
func Root() *cobra.Command {
	return rootCmd
}

// aggregate computes metrics for cmd, showing version lookup progress and
// lookup warnings on stderr.
func aggregate(cmd *cobra.Command, cfg config.Config, results []facts.ScanResult, lister metrics.VersionLister) metrics.Metrics {
	if lister == nil {
		return app.Aggregate(cmd.Context(), cfg, results, nil, app.Hooks{})
	}
	tr := ui.NewTracker(cmd.ErrOrStderr(), ui.Options{Title: "Looking up module versions", Noun: "modules", Live: live(cmd)})
	defer tr.Close()
	var once sync.Once
	return app.Aggregate(cmd.Context(), cfg, results, lister, app.Hooks{
		Lookup: func(done, total int) {
			once.Do(func() { tr.SetTotal(total) })
			if done > 0 {
				tr.Done(ui.Event{})
			}
		},
		Warn: tr.Warn,
	})
}

// specNames lists the sinks in specs: "json, openmetrics".
func specNames(specs []sink.Spec) string {
	names := make([]string, len(specs))
	for i, s := range specs {
		names[i] = s.Name
	}
	return strings.Join(names, ", ")
}

// plural returns noun, with an "s" unless n is 1.
func plural(n int, noun string) string {
	if n == 1 {
		return noun
	}
	return noun + "s"
}

// printAggregated reports what aggregation produced and where it went:
// styled when lv, the plain counts otherwise.
func printAggregated(cmd *cobra.Command, lv bool, repos int, m metrics.Metrics, specs []sink.Spec) error {
	return ui.Line(cmd.ErrOrStderr(), lv,
		fmt.Sprintf("%s Aggregated %s %s %s into %s series %s",
			ui.OK.Render(ui.IconOK), ui.Bold.Render(ui.Count(repos)), plural(repos, "repo"),
			ui.Subtle.Render(fmt.Sprintf("(%s with Terraform)", ui.Count(m.Repos))),
			ui.Bold.Render(ui.Count(len(m.Series))), ui.Subtle.Render("→ "+specNames(specs))),
		fmt.Sprintf("aggregated %d repos (%d with Terraform) into %d series", repos, m.Repos, len(m.Series)))
}
