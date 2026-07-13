// Command opencassette records, verifies and audits real LLM API cassettes.
package main

import (
	"os"

	"github.com/zereker/opencassette/internal/cli"
)

const version = "0.1.0"

func main() {
	os.Exit(cli.Execute(version, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
