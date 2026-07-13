package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/zereker/opencassette/internal/verify"
)

type verifyOptions struct {
	dir string
}

func newVerifyCommand(app *application) *cobra.Command {
	opts := verifyOptions{dir: "corpus"}
	cmd := &cobra.Command{
		Use:   "verify [directory]",
		Short: "Check cassettes for leaks and synthetic-data tells",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if len(args) == 1 {
				opts.dir = args[0]
			}

			return runVerifyCommand(app, opts)
		},
	}
	cmd.Flags().StringVar(&opts.dir, "dir", opts.dir, "corpus directory to verify")

	return cmd
}

func runVerifyCommand(app *application, opts verifyOptions) error {
	findings, files, err := verify.Dir(opts.dir)
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}

	for _, finding := range findings {
		_, _ = fmt.Fprintf(app.stdout, "%s  %s: %s\n", finding.Level, finding.Path, finding.Msg)
	}

	_, _ = fmt.Fprintf(app.stdout, "%d file(s) verified, %d finding(s)\n", files, len(findings))
	if verify.HasFailures(findings) {
		return exitCodeError{code: 1}
	}

	return nil
}
