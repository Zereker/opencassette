package verify

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func levels(fs []Finding) (fails, warns []string) {
	for _, f := range fs {
		if f.Level == Fail {
			fails = append(fails, f.Msg)
		} else {
			warns = append(warns, f.Msg)
		}
	}
	return
}

const goodTemplate = `meta:
  recorded_at: "%s"
  vendor: example
  tool: opencassette/test
interactions:
- request:
    body: '{"model":"m","messages":[{"role":"user","content":"hi"}]}'
    headers:
      Authorization:
      - '**REDACTED**'
    method: POST
    uri: https://api.example.com/v1/chat/completions
  response:
    body:
      string: '{"id":"chatcmpl-a1b2c3","created":1783840001,"choices":[{"message":{"content":"hello"}}],"usage":{"prompt_tokens":9,"completion_tokens":3,"total_tokens":12}}'
    headers:
      Content-Type:
      - application/json
    status:
      code: 200
      message: OK
`

func TestCleanFileHasNoFindings(t *testing.T) {
	ts := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	path := writeFile(t, fmt.Sprintf(goodTemplate, ts))
	fails, warns := levels(File(path))
	if len(fails) != 0 || len(warns) != 0 {
		t.Fatalf("clean file produced findings: fails=%v warns=%v", fails, warns)
	}
}

func TestUnscrubbedCredentialHeaderFails(t *testing.T) {
	ts := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	content := strings.Replace(fmt.Sprintf(goodTemplate, ts), "'**REDACTED**'", "'Bearer leaked-value'", 1)
	fails, _ := levels(File(writeFile(t, content)))
	if len(fails) == 0 || !strings.Contains(strings.Join(fails, ";"), "not scrubbed") {
		t.Fatalf("unscrubbed Authorization not caught: %v", fails)
	}
}

// TestCredentialHeaderVariantsAllCaught: a scalar-valued credential header
// (hand-written files use that form) must be flagged, and must not stop the
// remaining headers in the same section from being checked — an earlier
// version bailed out of the whole section on the first non-list value.
func TestCredentialHeaderVariantsAllCaught(t *testing.T) {
	ts := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	content := strings.Replace(fmt.Sprintf(goodTemplate, ts),
		"    headers:\n      Authorization:\n      - '**REDACTED**'\n",
		"    headers:\n      Authorization: Bearer scalar-leak\n      X-Api-Key:\n      - list-leak\n", 1)
	fails, _ := levels(File(writeFile(t, content)))
	joined := strings.Join(fails, ";")
	for _, header := range []string{"Authorization", "X-Api-Key"} {
		if !strings.Contains(joined, fmt.Sprintf("%q is not scrubbed", header)) {
			t.Errorf("leaked %s not caught: %v", header, fails)
		}
	}
}

// TestSecretShapedHeaderValueFails: a vendor's nonstandard auth header
// (name not on the credential list) recorded without -scrub-header must
// still be caught by the value-shape net.
func TestSecretShapedHeaderValueFails(t *testing.T) {
	ts := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	content := strings.Replace(fmt.Sprintf(goodTemplate, ts),
		"      Authorization:\n      - '**REDACTED**'\n",
		"      Authorization:\n      - '**REDACTED**'\n      X-Custom-Auth:\n      - 'sk-abcdefghijklmnopqrstuvwxyz123456'\n", 1)
	fails, _ := levels(File(writeFile(t, content)))
	if !strings.Contains(strings.Join(fails, ";"), `"X-Custom-Auth"`) {
		t.Fatalf("secret-shaped value in nonstandard header not caught: %v", fails)
	}
}

// TestSSEUsageChecked: usage arithmetic in a streaming response's final
// chunk gets the same scrutiny as a plain JSON body.
func TestSSEUsageChecked(t *testing.T) {
	ts := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	sse := `data: {"choices":[{"delta":{"content":"hi"}}]}\n\ndata: {"choices":[],"usage":{"prompt_tokens":9,"completion_tokens":3,"total_tokens":999}}\n\ndata: [DONE]\n\n`
	sse = strings.ReplaceAll(sse, `\n`, "\n        ") // YAML literal-block indent
	content := strings.Replace(fmt.Sprintf(goodTemplate, ts),
		`      string: '{"id":"chatcmpl-a1b2c3","created":1783840001,"choices":[{"message":{"content":"hello"}}],"usage":{"prompt_tokens":9,"completion_tokens":3,"total_tokens":12}}'`,
		"      string: |\n        "+sse, 1)
	_, warns := levels(File(writeFile(t, content)))
	if !strings.Contains(strings.Join(warns, ";"), "does not add up") {
		t.Fatalf("SSE usage inconsistency not caught: %v", warns)
	}
}

func TestSecretShapedStringsFail(t *testing.T) {
	ts := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	for name, replace := range map[string][2]string{
		"aws key in raw":     {"vendor: example", "vendor: AKIAABCDEFGHIJKLMNOP"},
		"sk key in request":  {`\"content\":\"hi\"`, `\"content\":\"sk-abcdefghijklmnopqrstuvwxyz123456\"`},
		"sk key in response": {`\"content\":\"hello\"`, `\"content\":\"sk-abcdefghijklmnopqrstuvwxyz123456\"`},
	} {
		content := fmt.Sprintf(goodTemplate, ts)
		// The template stores bodies as single-quoted YAML, so JSON quotes are
		// literal — build replacements accordingly.
		content = strings.Replace(content,
			strings.ReplaceAll(replace[0], `\"`, `"`),
			strings.ReplaceAll(replace[1], `\"`, `"`), 1)
		fails, _ := levels(File(writeFile(t, content)))
		if len(fails) == 0 {
			t.Errorf("%s: not caught", name)
		}
	}
}

func TestSyntheticTellsWarn(t *testing.T) {
	ts := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	content := fmt.Sprintf(goodTemplate, ts)
	content = strings.Replace(content, `"id":"chatcmpl-a1b2c3"`, `"id":"chatcmpl-verify-001"`, 1)
	content = strings.Replace(content, `"created":1783840001`, `"created":1234567890`, 1)
	content = strings.Replace(content, `"total_tokens":12`, `"total_tokens":999`, 1)
	_, warns := levels(File(writeFile(t, content)))
	joined := strings.Join(warns, ";")
	for _, want := range []string{"placeholder", "epoch placeholder", "does not add up"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing warn %q in %v", want, warns)
		}
	}
}

func TestImpossibleTimestampsFail(t *testing.T) {
	for _, ts := range []string{"2999-01-01T00:00:00Z", "2009-02-13T23:31:30Z", "not-a-date"} {
		fails, _ := levels(File(writeFile(t, fmt.Sprintf(goodTemplate, ts))))
		if len(fails) == 0 {
			t.Errorf("recorded_at %q accepted", ts)
		}
	}
}

func TestMissingMetaWarns(t *testing.T) {
	ts := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	content := fmt.Sprintf(goodTemplate, ts)
	content = content[strings.Index(content, "interactions:"):]
	_, warns := levels(File(writeFile(t, content)))
	if len(warns) == 0 || !strings.Contains(strings.Join(warns, ";"), "provenance") {
		t.Fatalf("missing meta not warned: %v", warns)
	}
}

func TestDirAggregatesAndCounts(t *testing.T) {
	dir := t.TempDir()
	ts := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	if err := os.WriteFile(filepath.Join(dir, "good.yaml"), []byte(fmt.Sprintf(goodTemplate, ts)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte("interactions: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	findings, files, err := Dir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if files != 2 {
		t.Errorf("files = %d", files)
	}
	if !HasFailures(findings) {
		t.Errorf("empty-interactions file should FAIL: %v", findings)
	}
}
