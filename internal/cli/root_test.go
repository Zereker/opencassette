package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func executeForTest(t *testing.T, args ...string) (int, string, string) {
	t.Helper()

	var stdout, stderr bytes.Buffer

	code := Execute("test", args, strings.NewReader(""), &stdout, &stderr)

	return code, stdout.String(), stderr.String()
}

func TestRootHelpAndVersion(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"help":            {args: []string{"--help"}, want: "Available Commands:"},
		"version flag":    {args: []string{"--version"}, want: "opencassette/test"},
		"legacy version":  {args: []string{"-version"}, want: "opencassette/test"},
		"version command": {args: []string{"version"}, want: "opencassette/test"},
	} {
		t.Run(name, func(t *testing.T) {
			code, stdout, stderr := executeForTest(t, tc.args...)
			if code != 0 || !strings.Contains(stdout, tc.want) || stderr != "" {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
		})
	}
}

func TestLegacyLongFlagIsAccepted(t *testing.T) {
	dir := t.TempDir()
	writeCleanCassette(t, dir)

	code, stdout, stderr := executeForTest(t, "verify", "-dir", dir)
	if code != 0 || !strings.Contains(stdout, "1 file(s) verified, 0 finding(s)") || stderr != "" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestVerifyFailureReturnsExitCodeWithoutTerminating(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte("interactions: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := executeForTest(t, "verify", dir)
	if code != 1 || !strings.Contains(stdout, "FAIL") || stderr != "" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestRecordValidationUsesCobraErrors(t *testing.T) {
	code, _, stderr := executeForTest(t, "record", "--auth", "none")
	if code != 1 || !strings.Contains(stderr, "--url is required") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func writeCleanCassette(t *testing.T, dir string) {
	t.Helper()

	ts := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)

	content := fmt.Sprintf(`meta:
  recorded_at: %q
interactions:
- request:
    body: '{}'
    headers: {}
    method: POST
    uri: https://api.example.com/v1
  response:
    body:
      string: '{}'
    headers: {}
    status:
      code: 200
      message: OK
`, ts)
	if err := os.WriteFile(filepath.Join(dir, "clean.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
