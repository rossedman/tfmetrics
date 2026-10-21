// Command tfmetrics statically parses Terraform repos and emits one consistent
// set of metrics.
package main

import "github.com/rossedman/tfmetrics/cmd"

// main runs the root command.
func main() {
	cmd.Execute()
}
