// Package cli contains the Cobra command tree for opencassette.
package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
)

type application struct {
	version string
	stdin   io.Reader
	stdout  io.Writer
	stderr  io.Writer
}

type exitCodeError struct {
	code int
}

func (e exitCodeError) Error() string { return fmt.Sprintf("exit status %d", e.code) }

// Execute runs the command tree and returns a process exit code. Keeping
// process termination here makes every subcommand directly testable.
func Execute(version string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	app := &application{version: version, stdin: stdin, stdout: stdout, stderr: stderr}
	root := newRootCommand(app)
	root.SetArgs(normalizeLegacyArgs(args))

	if err := root.Execute(); err != nil {
		var codeErr exitCodeError
		if errors.As(err, &codeErr) {
			return codeErr.code
		}

		_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)

		return 1
	}

	return 0
}

func newRootCommand(app *application) *cobra.Command {
	root := &cobra.Command{
		Use:           "opencassette",
		Short:         "Record, verify and audit real LLM API cassettes",
		Version:       app.version,
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	root.SetVersionTemplate("opencassette/{{.Version}}\n")
	root.SetIn(app.stdin)
	root.SetOut(app.stdout)
	root.SetErr(app.stderr)
	root.CompletionOptions.DisableDefaultCmd = true
	root.AddCommand(newRecordCommand(app), newVerifyCommand(app), newAuditCommand(app), newVersionCommand(app))

	return root
}

func newVersionCommand(app *application) *cobra.Command {
	return &cobra.Command{
		Use:    "version",
		Short:  "Print the opencassette version",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(app.stdout, "opencassette/%s\n", app.version)

			return err
		},
	}
}

// normalizeLegacyArgs keeps the original standard-library flag spelling
// working while Cobra establishes conventional double-dash long options.
func normalizeLegacyArgs(args []string) []string {
	known := map[string]bool{
		"append": true, "auth": true, "body-file": true, "bucket": true,
		"corpus-dir": true, "dir": true, "force": true, "header": true,
		"key-env": true, "model": true, "name": true, "out": true,
		"pause": true, "probe-fields": true, "protocol": true,
		"resolve": true, "scenario-dir": true, "scrub-header": true,
		"strict": true, "timeout": true, "url": true, "vendor": true,
		"version": true,
	}

	out := append([]string(nil), args...)
	for i, arg := range out {
		if !strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "--") || arg == "-h" {
			continue
		}

		name, _, _ := strings.Cut(strings.TrimPrefix(arg, "-"), "=")
		if known[name] {
			out[i] = "-" + arg
		}
	}

	return out
}
