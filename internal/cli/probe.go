package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zereker/opencassette/recorder"
	"github.com/zereker/opencassette/scenario"
)

// fieldSupport is the machine-readable support matrix a probe run writes
// next to the fields/ and fields-rejected/ trees: per field, whether the
// vendor accepted it in isolation, with the evidence cassette alongside.
type fieldSupport struct {
	RecordedAt   string                 `json:"recorded_at"`
	Vendor       string                 `json:"vendor"`
	Model        string                 `json:"model"`
	Endpoint     string                 `json:"endpoint"`
	Source       string                 `json:"source"`
	SourceSHA256 string                 `json:"source_sha256"`
	Fields       map[string]fieldResult `json:"fields"`
}

type fieldResult struct {
	Status     string   `json:"status"` // supported | rejected | error
	HTTP       int      `json:"http,omitempty"`
	Companions []string `json:"companions,omitempty"`
}

func runProbe(run runConfig, dir, corpusDir, vendor, model, protocol string) error {
	baseRaw, err := os.ReadFile(filepath.Join(dir, "chat_basic.json"))
	if err != nil {
		return fmt.Errorf("record: probe base: %w", err)
	}

	base, err := scenario.Scenario{Name: "chat_basic", Body: baseRaw, ModelField: "model"}.WithModel(model)
	if err != nil {
		return fmt.Errorf("record: %w", err)
	}

	fullRaw, err := os.ReadFile(filepath.Join(dir, "chat_full_params.json"))
	if err != nil {
		return fmt.Errorf("record: probe field source: %w", err)
	}

	fullSHA := scenario.Scenario{Body: fullRaw}.SHA256()

	probes, err := scenario.BuildProbes(base, fullRaw)
	if err != nil {
		return fmt.Errorf("record: %w", err)
	}

	// Refuse to clobber an earlier probe run before spending any API
	// calls — the per-file check in writeCassette would only trip after
	// the baseline and first probes already ran.
	protoDirEarly := filepath.Join(corpusDir, vendor, model, protocol)

	if !run.force {
		for _, p := range []string{"fields", "fields-rejected", "field-support.json"} {
			if _, err := os.Stat(filepath.Join(protoDirEarly, p)); err == nil {
				return fmt.Errorf("record: %s already has probe results (%s) — re-probe with --force", protoDirEarly, p)
			}
		}
	}

	// If the minimal body itself fails, every probe would read as a
	// rejection — abort instead of writing a matrix of noise.
	_, _ = fmt.Fprintln(run.stderr, "===== baseline: minimal request =====")

	status, _, err := probeOne(run, base, recorder.Meta{})
	if err != nil {
		return fmt.Errorf("record: baseline call failed (nothing probed): %w", err)
	}

	if status < 200 || status >= 300 {
		return fmt.Errorf("record: baseline minimal request got HTTP %d — fix endpoint/model/auth before probing fields", status)
	}

	protoDir := filepath.Join(corpusDir, vendor, model, protocol)
	report := fieldSupport{
		RecordedAt:   time.Now().UTC().Format(time.RFC3339),
		Vendor:       vendor,
		Model:        model,
		Endpoint:     hostOf(run.endpoint),
		Source:       "chat_full_params.json",
		SourceSHA256: fullSHA,
		Fields:       map[string]fieldResult{},
	}

	var errored []string

	for _, p := range probes {
		time.Sleep(run.pause)
		_, _ = fmt.Fprintf(run.stderr, "\n===== field %s =====\n", p.Field)

		res := fieldResult{Companions: p.Companions}
		if strings.ContainsAny(p.Field, `/\`) {
			_, _ = fmt.Fprintf(run.stderr, "ERROR: field name %q is not a path segment\n", p.Field)

			res.Status = "error"
			report.Fields[p.Field] = res
			errored = append(errored, p.Field)

			continue
		}

		meta := metaFor(run.endpoint, vendor, model, "field:"+p.Field, fullSHA, run.version)
		status, rec, err := probeOne(run, p.Body, meta)

		res.HTTP = status
		switch {
		case err != nil:
			_, _ = fmt.Fprintf(run.stderr, "ERROR: %v\n", err)

			res.Status = "error"

			errored = append(errored, p.Field)
		case status >= 200 && status < 300:
			res.Status = "supported"

			path := filepath.Join(protoDir, "fields", p.Field+".yaml")
			if err := run.writeCassette(rec, path); err != nil {
				return fmt.Errorf("record: %w", err)
			}

			_, _ = fmt.Fprintf(run.stderr, "SUPPORTED — wrote %s\n", path)
		case status == 400 || status == 422:
			res.Status = "rejected"

			path := filepath.Join(protoDir, "fields-rejected", p.Field+".yaml")
			if err := run.writeCassette(rec, path); err != nil {
				return fmt.Errorf("record: %w", err)
			}

			_, _ = fmt.Fprintf(run.stderr, "REJECTED — wrote %s\n", path)
		default:
			// 401/403/429/5xx say nothing about the field itself: record
			// no evidence, claim neither support nor rejection.
			res.Status = "error"

			errored = append(errored, fmt.Sprintf("%s (HTTP %d)", p.Field, status))
		}

		report.Fields[p.Field] = res
	}

	if err := os.MkdirAll(protoDir, 0o750); err != nil {
		return fmt.Errorf("record: mkdir %s: %w", protoDir, err)
	}

	buf, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("record: marshal support matrix: %w", err)
	}

	reportPath := filepath.Join(protoDir, "field-support.json")
	if err := os.WriteFile(reportPath, append(buf, '\n'), 0o644); err != nil {
		return fmt.Errorf("record: write %s: %w", reportPath, err)
	}

	supported, rejected := 0, 0

	for _, r := range report.Fields {
		switch r.Status {
		case "supported":
			supported++
		case "rejected":
			rejected++
		}
	}

	_, _ = fmt.Fprintf(run.stderr, "\n%d field(s): %d supported, %d rejected, %d error(s) — matrix in %s\n",
		len(probes), supported, rejected, len(errored), reportPath)

	if len(errored) > 0 {
		_, _ = fmt.Fprintf(run.stderr, "no evidence recorded for: %s\n", strings.Join(errored, ", "))
	}

	_, _ = fmt.Fprintln(run.stderr, "before publishing: read the files, then run `opencassette verify` over them")

	if len(errored) > 0 {
		return exitCodeError{code: 1}
	}

	return nil
}

// probeOne sends one probe request through a fresh recorder and reports the
// upstream status; the caller decides which bucket (if any) the recording
// lands in — unlike recordOne, a 4xx here is data, not an operator mistake.
func probeOne(run runConfig, body []byte, meta recorder.Meta) (int, *recorder.Recorder, error) {
	rec := run.newRecorder()
	rec.SetMeta(meta)

	req, err := buildRequest(run, body, rec)
	if err != nil {
		return 0, nil, err
	}

	client := &http.Client{Transport: rec, Timeout: run.timeout}

	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}

	respBody, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if err != nil {
		return 0, nil, fmt.Errorf("read response: %w", err)
	}

	_, _ = fmt.Fprintf(run.stderr, "HTTP %s\n%s\n", resp.Status, preview(respBody, 800))

	return resp.StatusCode, rec, nil
}
