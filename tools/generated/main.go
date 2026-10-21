// Command generated prints the import-path-qualified names of generated Go
// files, one per line, so the coverage gate can exclude them.
package main

import (
	"fmt"
	"os"

	"github.com/rossedman/tfmetrics/tools/internal/gofiles"
)

// main walks the packages named on the command line.
func main() {
	files, err := gofiles.Walk(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "generated:", err)
		os.Exit(2)
	}
	for _, f := range files {
		if f.Generated && !f.Test {
			fmt.Println(f.ImportPath)
		}
	}
}
