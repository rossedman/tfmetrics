// Command gendocs writes one Markdown reference page per tfmetrics command
// into docs/reference/commands, so the docs never drift from the CLI.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra/doc"

	"github.com/rossedman/tfmetrics/cmd"
)

// main regenerates the command reference.
func main() {
	dir := "docs/reference/commands"
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}
	if err := generate(dir); err != nil {
		fmt.Fprintln(os.Stderr, "gendocs:", err)
		os.Exit(1)
	}
}

// generate clears dir and writes the pages.
func generate(dir string) error {
	old, _ := filepath.Glob(filepath.Join(dir, "*.md"))
	for _, f := range old {
		if err := os.Remove(f); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	root := cmd.Root()
	root.DisableAutoGenTag = true
	prepend := func(string) string { return "" }
	link := func(name string) string { return strings.ToLower(name) }
	if err := doc.GenMarkdownTreeCustom(root, dir, prepend, link); err != nil {
		return err
	}
	return linkConfig(dir)
}

// configNote is appended to every page that shows the --config flag.
const configNote = "\nEvery key the config file accepts, and which flag or environment variable overrides it, is in the [config file reference](../config.md).\n"

// linkConfig appends configNote to each page in dir that mentions --config.
func linkConfig(dir string) error {
	pages, _ := filepath.Glob(filepath.Join(dir, "*.md"))
	for _, p := range pages {
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if !strings.Contains(string(b), "--config") {
			continue
		}
		if err := os.WriteFile(p, append(b, configNote...), 0o644); err != nil {
			return err
		}
	}
	return nil
}
