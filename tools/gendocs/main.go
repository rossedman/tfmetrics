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
	return doc.GenMarkdownTreeCustom(root, dir, prepend, link)
}
