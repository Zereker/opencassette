package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zereker/opencassette/recorder"
)

func TestResolveOutPathAndAppendBucket(t *testing.T) {
	dir := t.TempDir()

	streamPath, err := resolveOutPath("", dir, "vendor", "model", "openai", "chat", true, false)
	if err != nil {
		t.Fatal(err)
	}

	if want := filepath.Join(dir, "vendor", "model", "openai", "stream", "chat.yaml"); streamPath != want {
		t.Fatalf("path=%q want=%q", streamPath, want)
	}

	// WriteFile needs an interaction, so create the sibling as an ordinary
	// file; resolveOutPath only needs to discover its location.
	if err := os.MkdirAll(filepath.Dir(streamPath), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(streamPath, []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := resolveOutPath("", dir, "vendor", "model", "openai", "chat", false, true)
	if err != nil {
		t.Fatal(err)
	}

	if got != streamPath {
		t.Fatalf("append path=%q want existing sibling %q", got, streamPath)
	}
}

func TestBuildRequestAuthAndHeaders(t *testing.T) {
	rec := recorder.New(nil)
	run := runConfig{
		endpoint:  "https://api.example.com/v1?existing=yes",
		authStyle: "query:key",
		key:       "secret",
		headers:   []string{"X-Test: value"},
		stderr:    &bytes.Buffer{},
	}

	req, err := buildRequest(run, []byte(`{"hello":"world"}`), rec)
	if err != nil {
		t.Fatal(err)
	}

	if req.URL.Query().Get("key") != "secret" || req.URL.Query().Get("existing") != "yes" {
		t.Fatalf("query=%q", req.URL.RawQuery)
	}

	if req.Header.Get("X-Test") != "value" || req.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("headers=%v", req.Header)
	}

	run.authStyle = "invalid"
	if _, err := buildRequest(run, nil, rec); err == nil || !strings.Contains(err.Error(), "unknown --auth") {
		t.Fatalf("invalid auth error=%v", err)
	}
}
