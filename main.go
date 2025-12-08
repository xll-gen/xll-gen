package main

import "xll-gen/cmd"

// main is the entry point of the xll-gen CLI application.
// It executes the root command which handles argument parsing and subcommand dispatch.
func main() {
	cmd.Execute()
}
