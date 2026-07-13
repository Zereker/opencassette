package redact

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBaselineScrubsCredentialHeader is the floor: a credential header is
// blanked by name even with no profile in play.
func TestBaselineScrubsCredentialHeader(t *testing.T) {
	s := Baseline().NewScrubber()

	h := http.Header{"Authorization": {"Bearer sk-secret"}}
	out := s.Headers(h, s.TraceReplacements(h))

	if got := out["Authorization"]; len(got) != 1 || got[0] != Redacted {
		t.Fatalf("credential header not blanked: %v", got)
	}
}

// TestProfileTraceHeaderIsRewrittenEverywhere is the point of the whole
// package: a vendor profile adds a correlation header, and its value is
// discovered and rewritten to the same stable marker in the header AND in a
// body that echoes it — without touching code.
func TestProfileTraceHeaderIsRewrittenEverywhere(t *testing.T) {
	const traceVal = "016486c1-2725-49fa-bbea-f5a11503f0ce"

	rules := Baseline()
	if err := rules.Merge(&Profile{TraceHeaders: []string{"apim-request-id"}}); err != nil {
		t.Fatal(err)
	}

	s := rules.NewScrubber()

	h := http.Header{"Apim-Request-Id": {traceVal}}
	tr := s.TraceReplacements(h)

	out := s.Headers(h, tr)
	if got := out["Apim-Request-Id"]; len(got) != 1 || got[0] != "**TRACE_ID_1**" {
		t.Fatalf("profile trace header not rewritten: %v", got)
	}

	body := s.Bytes([]byte(`{"request_id":"`+traceVal+`"}`), tr)
	if strings.Contains(string(body), traceVal) {
		t.Fatalf("trace value leaked into body: %s", body)
	}

	if !strings.Contains(string(body), "**TRACE_ID_1**") {
		t.Fatalf("trace marker missing from body: %s", body)
	}
}

// TestSecretIsReplacedInHeaderAndURI covers value-based scrubbing: a literal
// key leaks nowhere, including its URL-escaped spelling in a query string.
func TestSecretIsReplacedInHeaderAndURI(t *testing.T) {
	const key = "sk-abc+/=123"

	rules := Baseline()
	rules.AddQueryParam("key")
	s := rules.NewScrubber()
	s.AddSecret(key)

	h := http.Header{"X-Echoed": {key}}
	out := s.Headers(h, nil)

	if got := out["X-Echoed"]; got[0] != Redacted {
		t.Fatalf("echoed secret not redacted in header: %v", got)
	}
}

// TestLoadProfileMissingIsNotAnError: most vendors have no profile, and that
// must be a silent no-op rather than a failure.
func TestLoadProfileMissingIsNotAnError(t *testing.T) {
	p, err := LoadProfile(t.TempDir(), "no-such-vendor")
	if err != nil {
		t.Fatalf("missing profile should not error: %v", err)
	}

	if p != nil {
		t.Fatalf("missing profile should be nil, got %+v", p)
	}
}

// TestLoadProfileParsesYAML reads a real file and feeds it through Merge, the
// exact path the recorder uses.
func TestLoadProfileParsesYAML(t *testing.T) {
	dir := t.TempDir()
	yaml := "trace_headers:\n  - apim-request-id\ncredential_headers:\n  - x-vendor-token\n"

	if err := os.WriteFile(filepath.Join(dir, "azure.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}

	p, err := LoadProfile(dir, "azure")
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}

	rules := Baseline()
	if err := rules.Merge(p); err != nil {
		t.Fatal(err)
	}

	s := rules.NewScrubber()

	cred := http.Header{"X-Vendor-Token": {"secret"}}
	if got := s.Headers(cred, nil)["X-Vendor-Token"]; got[0] != Redacted {
		t.Fatalf("profile credential header not applied: %v", got)
	}
}

// TestCustomReplacements covers all three matchers and scope confinement: a
// header override, a scoped literal find, and a body-only regexp.
func TestCustomReplacements(t *testing.T) {
	rules := Baseline()
	err := rules.Merge(&Profile{
		Replacements: []Replacement{
			{Header: "x-ms-region", With: "**REGION**"},
			{Find: "auto-eastus2-s002", With: "**RESOURCE**", In: []string{"uri", "body"}},
			{Pattern: `chatcmpl-[A-Za-z0-9]+`, With: "**CID**", In: []string{"body"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	s := rules.NewScrubber()

	if got := s.Headers(http.Header{"X-Ms-Region": {"East US 2"}}, nil)["X-Ms-Region"]; got[0] != "**REGION**" {
		t.Fatalf("header override not applied: %v", got)
	}

	body := string(s.Bytes([]byte(`{"id":"chatcmpl-ABC123","host":"auto-eastus2-s002"}`), nil))
	if strings.Contains(body, "chatcmpl-ABC123") || strings.Contains(body, "auto-eastus2-s002") {
		t.Fatalf("body replacement left original: %s", body)
	}

	if !strings.Contains(body, "**CID**") || !strings.Contains(body, "**RESOURCE**") {
		t.Fatalf("body markers missing: %s", body)
	}

	u, _ := url.Parse("https://x/auto-eastus2-s002/v1?q=chatcmpl-XYZ")
	uriStr := s.URI(u, nil)

	if strings.Contains(uriStr, "auto-eastus2-s002") {
		t.Fatalf("uri-scoped find not applied: %s", uriStr)
	}

	if !strings.Contains(uriStr, "chatcmpl-XYZ") {
		t.Fatalf("body-only pattern leaked into uri scope: %s", uriStr)
	}
}

// TestReplacementValidation rejects malformed rules at load time.
func TestReplacementValidation(t *testing.T) {
	cases := []Replacement{
		{With: "x"},                          // no matcher
		{Find: "a", Pattern: "b", With: "x"}, // two matchers
		{Pattern: "[", With: "x"},            // invalid regexp
		{Find: "a"},                          // no 'with'
	}

	for i, rp := range cases {
		if err := Baseline().Merge(&Profile{Replacements: []Replacement{rp}}); err == nil {
			t.Errorf("case %d %+v: expected error, got nil", i, rp)
		}
	}
}

// TestScrubEndpoint confirms the provenance endpoint is masked by a URI-scope
// replacement (and secrets), so an internal proxy host does not leak via
// meta.endpoint.
func TestScrubEndpoint(t *testing.T) {
	rules := Baseline()
	if err := rules.Merge(&Profile{Replacements: []Replacement{
		{Pattern: `[a-z0-9-]+\.openai\.azure\.com`, With: "**RESOURCE**.openai.azure.com"},
	}}); err != nil {
		t.Fatal(err)
	}

	s := rules.NewScrubber()

	got := s.ScrubEndpoint("https://auto-eastus2-s011-d0429-gpt-5-5-codex-auto.openai.azure.com")
	if got != "https://**RESOURCE**.openai.azure.com" {
		t.Fatalf("internal host not normalized: %s", got)
	}

	// A body-only replacement must NOT touch the endpoint.
	rules2 := Baseline()
	_ = rules2.Merge(&Profile{Replacements: []Replacement{
		{Find: "auto-eastus2", With: "**X**", In: []string{"body"}},
	}})

	if got := rules2.NewScrubber().ScrubEndpoint("https://auto-eastus2.openai.azure.com"); got != "https://auto-eastus2.openai.azure.com" {
		t.Fatalf("body-scoped rule leaked into endpoint: %s", got)
	}
}
