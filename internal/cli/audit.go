package cli

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zereker/opencassette/internal/audit"
	"github.com/zereker/opencassette/internal/scenario"
)

type auditOptions struct {
	dir     string
	strict  bool
	resolve bool
	timeout time.Duration
}

func newAuditCommand(app *application) *cobra.Command {
	opts := auditOptions{dir: "packs", timeout: time.Minute}
	cmd := &cobra.Command{
		Use:   "audit [pack-directory]",
		Short: "Diff scenario coverage against authoritative protocol specs",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if len(args) == 1 {
				opts.dir = args[0]
			}

			return runAuditCommand(app, opts)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&opts.dir, "dir", opts.dir, "packs root, or a single pack directory")
	flags.BoolVar(&opts.strict, "strict", false, "exit 1 if any audited pack is missing spec-declared fields")
	flags.BoolVar(&opts.resolve, "resolve", false, "print each pack's resolved, pinnable spec URL")
	flags.DurationVar(&opts.timeout, "timeout", opts.timeout, "spec fetch timeout")

	return cmd
}

func runAuditCommand(app *application, opts auditOptions) error {
	dirs := []string{opts.dir}
	if _, err := os.Stat(filepath.Join(opts.dir, "pack.json")); os.IsNotExist(err) {
		entries, readErr := os.ReadDir(opts.dir)
		if readErr != nil {
			return fmt.Errorf("audit: %w", readErr)
		}

		dirs = dirs[:0]

		for _, entry := range entries {
			if entry.IsDir() {
				dirs = append(dirs, filepath.Join(opts.dir, entry.Name()))
			}
		}
	}

	scanning := len(dirs) != 1 || dirs[0] != opts.dir
	client := &http.Client{Timeout: opts.timeout}
	gaps := false

	for _, dir := range dirs {
		pack, err := scenario.LoadPack(dir)
		if err != nil {
			if scanning {
				_, _ = fmt.Fprintf(app.stdout, "== %s: not a loadable pack, skipping (%v)\n", dir, err)
				continue
			}

			return fmt.Errorf("audit: %w", err)
		}

		if pack.Spec == nil {
			_, _ = fmt.Fprintf(app.stdout, "== %s (%s): no spec declared in pack.json, skipping\n", dir, pack.Protocol)
			continue
		}

		if opts.resolve {
			resolvedURL := pack.Spec.URL
			if pack.Spec.Kind == "stainless-stats" {
				resolvedURL, err = audit.StainlessSpecURL(client, resolvedURL)
				if err != nil {
					return fmt.Errorf("audit: %s: %w", dir, err)
				}

				_, _ = fmt.Fprintf(app.stdout, "%s: pin as {\"kind\": \"openapi\", \"url\": %q, \"path\": %q}\n", dir, resolvedURL, pack.Spec.Path)

				continue
			}

			_, _ = fmt.Fprintf(app.stdout, "%s: %s (already a stable URL)\n", dir, resolvedURL)

			continue
		}

		specFields, err := audit.Fields(client, pack.Spec)
		if err != nil {
			return fmt.Errorf("audit: %s: %w", dir, err)
		}

		if pack.ModelField == "" {
			specFields = without(specFields, "model")
		}

		report := audit.Compare(audit.PackFields(pack), specFields)
		_, _ = fmt.Fprintf(app.stdout, "== %s (%s) vs %s\n", dir, pack.Protocol, pack.Spec.URL)
		_, _ = fmt.Fprintf(app.stdout, "   covered %d/%d spec fields\n", len(report.Covered), report.SpecTotal)

		if len(report.Missing) > 0 {
			gaps = true
			_, _ = fmt.Fprintf(app.stdout, "   missing from pack (%d): %s\n", len(report.Missing), strings.Join(report.Missing, ", "))
		}

		if len(report.Extra) > 0 {
			_, _ = fmt.Fprintf(app.stdout, "   not in spec (%d, vendor extension or spec drift): %s\n", len(report.Extra), strings.Join(report.Extra, ", "))
		}
	}

	if opts.strict && gaps {
		return exitCodeError{code: 1}
	}

	return nil
}

func without(list []string, drop string) []string {
	out := make([]string, 0, len(list))
	for _, value := range list {
		if value != drop {
			out = append(out, value)
		}
	}

	return out
}
