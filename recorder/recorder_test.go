package recorder

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zereker/opencassette/cassette"
)

const testSecret = "sk-test-secret-4f9a2b"

// TestRecordAndLoadRoundTrip records a JSON exchange and an SSE exchange
// against a real (test) HTTP server, writes the cassette with a meta block,
// and loads it back through the actual cassette.Load — proving the write
// side and the read side agree on the on-disk format, and that no trace of
// the API key survives anywhere in the file.
func TestRecordAndLoadRoundTrip(t *testing.T) {
	jsonReply := `{"id":"real-1","choices":[{"message":{"role":"assistant","content":"hi"}}]}`
	sseReply := "data: {\"choices\":[{\"delta\":{\"content\":\"h\"}}]}\n\ndata: [DONE]\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "stream") {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, sseReply)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// A response header echoing the key back (some vendors do) must be
		// caught by the value-based scrub, not just the name-based one.
		w.Header().Set("X-Echoed-Auth", testSecret)
		_, _ = io.WriteString(w, jsonReply)
	}))
	defer srv.Close()

	rec := New(nil)
	rec.RedactValue(testSecret)
	rec.SetMeta(Meta{RecordedAt: "2026-07-12T08:00:00Z", Vendor: "example", Model: "m", Tool: "opencassette/test"})
	client := &http.Client{Transport: rec}

	reqBody := `{"model":"m","messages":[{"role":"user","content":"hi"}]}`
	req1, _ := http.NewRequest("POST", srv.URL+"/chat?key="+testSecret, strings.NewReader(reqBody))
	req1.Header.Set("Authorization", "Bearer "+testSecret)
	resp1, err := client.Do(req1)
	if err != nil {
		t.Fatalf("request 1: %v", err)
	}
	got1, _ := io.ReadAll(resp1.Body)
	_ = resp1.Body.Close()
	if string(got1) != jsonReply {
		t.Fatalf("caller must still see the real body, got %q", got1)
	}

	req2, _ := http.NewRequest("POST", srv.URL+"/chat/stream", strings.NewReader(reqBody))
	req2.Header.Set("Authorization", "Bearer "+testSecret)
	if _, err := client.Do(req2); err != nil {
		t.Fatalf("request 2: %v", err)
	}

	path := filepath.Join(t.TempDir(), "nested", "recorded.yaml")
	if err := rec.WriteFile(path); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if bytes.Contains(raw, []byte(testSecret)) {
		t.Fatalf("the API key leaked into the cassette:\n%s", raw)
	}
	if !bytes.Contains(raw, []byte(redacted)) {
		t.Fatalf("expected %q markers in the cassette:\n%s", redacted, raw)
	}
	if !bytes.Contains(raw, []byte("recorded_at: \"2026-07-12T08:00:00Z\"")) &&
		!bytes.Contains(raw, []byte(`recorded_at: 2026-07-12T08:00:00Z`)) {
		t.Fatalf("meta block missing:\n%s", raw)
	}

	its, err := cassette.Load(path)
	if err != nil {
		t.Fatalf("cassette.Load: %v", err)
	}
	if len(its) != 2 {
		t.Fatalf("want 2 interactions, got %d", len(its))
	}
	if its[0].Method != "POST" || string(its[0].RequestBody) != reqBody {
		t.Errorf("interaction 0 request mismatch: method=%q body=%q", its[0].Method, its[0].RequestBody)
	}
	if string(its[0].ResponseBody) != jsonReply {
		t.Errorf("interaction 0 response mismatch: %q", its[0].ResponseBody)
	}
	if !strings.Contains(its[0].URI, "key="+redacted) && !strings.Contains(its[0].URI, "key=%2A%2AREDACTED%2A%2A") {
		t.Errorf("query-string key not redacted in URI: %q", its[0].URI)
	}
	if string(its[1].ResponseBody) != sseReply {
		t.Errorf("interaction 1 (SSE) response mismatch: %q", its[1].ResponseBody)
	}
}

// TestAppendAcrossRuns writes one interaction, then a second Recorder
// prepends that file and records another — simulating turn 2 of a tool-call
// loop in a separate invocation. The first run's meta block must survive.
func TestAppendAcrossRuns(t *testing.T) {
	var turn int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		turn++
		w.Header().Set("Content-Type", "application/json")
		if turn == 1 {
			_, _ = io.WriteString(w, `{"turn":1}`)
		} else {
			_, _ = io.WriteString(w, `{"turn":2}`)
		}
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "loop.yaml")

	rec1 := New(nil)
	rec1.SetMeta(Meta{RecordedAt: "2026-07-12T08:00:00Z", Scenario: "loop"})
	client1 := &http.Client{Transport: rec1}
	if _, err := client1.Post(srv.URL, "application/json", strings.NewReader(`{"n":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := rec1.WriteFile(path); err != nil {
		t.Fatal(err)
	}

	rec2 := New(nil)
	if err := rec2.PrependFromFile(path); err != nil {
		t.Fatalf("PrependFromFile: %v", err)
	}
	client2 := &http.Client{Transport: rec2}
	if _, err := client2.Post(srv.URL, "application/json", strings.NewReader(`{"n":2}`)); err != nil {
		t.Fatal(err)
	}
	if err := rec2.WriteFile(path); err != nil {
		t.Fatal(err)
	}

	its, err := cassette.Load(path)
	if err != nil {
		t.Fatalf("cassette.Load: %v", err)
	}
	if len(its) != 2 {
		t.Fatalf("want 2 interactions after append, got %d", len(its))
	}
	if string(its[0].ResponseBody) != `{"turn":1}` || string(its[1].ResponseBody) != `{"turn":2}` {
		t.Errorf("order not preserved: %q, %q", its[0].ResponseBody, its[1].ResponseBody)
	}
	raw, _ := os.ReadFile(path)
	if !bytes.Contains(raw, []byte("scenario: loop")) {
		t.Errorf("meta block lost across append:\n%s", raw)
	}
}

// TestBinaryBodyRoundTrip covers a non-UTF-8 response body (e.g. AWS
// event-stream framing): it must come back byte-identical through the
// !!binary YAML representation.
func TestBinaryBodyRoundTrip(t *testing.T) {
	// Deliberately not gzip magic (0x1f 0x8b) — cassette.Load transparently
	// gunzips that, which would be a different (also valid) path.
	binary := []byte{0xff, 0xfe, 0x00, 0x01, 'e', 'v', 'e', 'n', 't', 0x80}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		_, _ = w.Write(binary)
	}))
	defer srv.Close()

	rec := New(nil)
	client := &http.Client{Transport: rec}
	if _, err := client.Post(srv.URL, "application/json", strings.NewReader(`{}`)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "bin.yaml")
	if err := rec.WriteFile(path); err != nil {
		t.Fatal(err)
	}
	its, err := cassette.Load(path)
	if err != nil {
		t.Fatalf("cassette.Load: %v", err)
	}
	if !bytes.Equal(its[0].ResponseBody, binary) {
		t.Errorf("binary body did not round-trip: got %x want %x", its[0].ResponseBody, binary)
	}
}
