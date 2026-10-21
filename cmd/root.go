// Package cmd holds the tfmetrics cobra commands, one per file.
package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"

	"github.com/rossedman/tfmetrics/internal/app"
	"github.com/rossedman/tfmetrics/internal/sink"
)

var (
	cfgFile string
	v       = app.NewViper()
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

// Execute runs the root command and exits non-zero on error.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// init registers the persistent flags.
func init() {
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default: tfmetrics.yaml in $XDG_CONFIG_HOME/tfmetrics, $HOME or the current directory)")
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
	} else {
		v.SetConfigName("tfmetrics")
		v.SetConfigType("yaml")
		if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
			v.AddConfigPath(filepath.Join(x, "tfmetrics"))
		}
		if h, err := os.UserHomeDir(); err == nil {
			v.AddConfigPath(filepath.Join(h, ".config", "tfmetrics"))
			v.AddConfigPath(h)
		}
		v.AddConfigPath(".")
	}
	if err := v.ReadInConfig(); err != nil {
		var nf viper.ConfigFileNotFoundError
		if cfgFile != "" || !errors.As(err, &nf) {
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
