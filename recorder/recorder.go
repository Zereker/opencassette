// Package recorder records real HTTP interactions into cassette YAML files
// the sibling cassette package loads back. It is an http.RoundTripper: wrap
// any http.Client's transport, make real API calls, then WriteFile — the
// output is pytest-recording's `interactions:` format plus a `meta:`
// provenance block (see Meta), with credentials scrubbed to `**REDACTED**`.
//
// Scrubbing is both name-based (a default set of credential-bearing header
// names, extendable via ScrubHeader/ScrubQueryParam) and value-based
// (RedactValue registers the literal API key, which is then replaced
// wherever its bytes appear — any header, any query parameter, including
// URL-escaped spellings — so a vendor with a nonstandard auth header can't
// leak the key just because the header name wasn't on the list).
//
// A recorded file must still be checked by a human before publishing:
// scrubbing removes the credentials this package knows about, not secrets a
// response body might echo back. Run the verify package over the output as
// a second net.
package recorder

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const redacted = "**REDACTED**"

// defaultScrubHeaders are the credential-bearing header names every
// recording scrubs regardless of configuration.
var defaultScrubHeaders = []string{
	"authorization", "proxy-authorization",
	"x-api-key", "api-key", "x-goog-api-key", "x-auth-token",
	"x-amz-security-token",
	"cookie", "set-cookie",
}

// Meta is the provenance block written at the top of every recording — the
// difference between a verifiable capture and an unattributable blob. The
// verify package warns on files without one.
type Meta struct {
	RecordedAt string `yaml:"recorded_at"`        // RFC3339 UTC, stamped at write time
	Vendor     string `yaml:"vendor,omitempty"`   // corpus vendor segment
	Model      string `yaml:"model,omitempty"`    // model as sent upstream
	Endpoint   string `yaml:"endpoint,omitempty"` // scheme://host of the real API called
	Scenario   string `yaml:"scenario,omitempty"` // scenario-pack name, if batch-recorded
	// ScenarioSHA256 is the hex SHA-256 of the pack scenario body as
	// committed (pre model-substitution), so a cassette stays traceable to
	// the exact pack version that produced it after the pack file changes.
	ScenarioSHA256 string `yaml:"scenario_sha256,omitempty"`
	Tool           string `yaml:"tool,omitempty"` // recorder identifier, e.g. opencassette/0.1.0
}

// Recorder is an http.RoundTripper that passes requests through to a base
// transport while accumulating scrubbed copies of every exchange. It is
// safe for concurrent use; interactions are appended in completion order.
type Recorder struct {
	base http.RoundTripper

	mu           sync.Mutex
	meta         *Meta
	interactions []yaml.Node
	scrubHeader  map[string]bool
	scrubQuery   map[string]bool
	secretValues []string
}

// New wraps base (nil = http.DefaultTransport) in a recording transport.
func New(base http.RoundTripper) *Recorder {
	if base == nil {
		base = http.DefaultTransport
	}
	r := &Recorder{
		base:        base,
		scrubHeader: make(map[string]bool, len(defaultScrubHeaders)),
		scrubQuery:  map[string]bool{},
	}
	for _, h := range defaultScrubHeaders {
		r.scrubHeader[h] = true
	}
	return r
}

// SetMeta attaches the provenance block WriteFile will emit.
func (r *Recorder) SetMeta(m Meta) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.meta = &m
}

// RedactValue registers a literal secret (an API key) to be replaced with
// **REDACTED** wherever its exact bytes appear — header values and the
// request URI — regardless of which header or query parameter carried it.
func (r *Recorder) RedactValue(v string) {
	if v != "" {
		r.secretValues = append(r.secretValues, v)
	}
}

// ScrubHeader adds a header name (case-insensitive) to the redaction set.
func (r *Recorder) ScrubHeader(name string) { r.scrubHeader[strings.ToLower(name)] = true }

// ScrubQueryParam adds a query parameter name to the redaction set (for
// key-in-URL auth styles like Gemini AI Studio's ?key=...).
func (r *Recorder) ScrubQueryParam(name string) { r.scrubQuery[name] = true }

// Len reports how many interactions have been recorded so far.
func (r *Recorder) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.interactions)
}

// RoundTrip implements http.RoundTripper: it forwards the request on the
// base transport, buffers the full response body (so recording a streaming
// SSE response just means waiting for EOF), stores a scrubbed copy of the
// exchange, and hands the caller a replayable response.
func (r *Recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	var reqBody []byte
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("recorder: read request body: %w", err)
		}
		reqBody = b
		req.Body = io.NopCloser(bytes.NewReader(b))
	}

	resp, err := r.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	respBody, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("recorder: read response body: %w", err)
	}
	resp.Body = io.NopCloser(bytes.NewReader(respBody))

	doc := interactionDoc{
		Request: requestDoc{
			Body:    bodyValue(reqBody),
			Headers: r.scrubHeaders(req.Header),
			Method:  req.Method,
			URI:     r.scrubURI(req.URL),
		},
		Response: responseDoc{
			Body:    respBodyDoc{String: bodyValue(respBody)},
			Headers: r.scrubHeaders(resp.Header),
			Status:  statusDoc{Code: resp.StatusCode, Message: statusMessage(resp)},
		},
	}
	var node yaml.Node
	if err := node.Encode(doc); err != nil {
		return nil, fmt.Errorf("recorder: encode interaction: %w", err)
	}

	r.mu.Lock()
	r.interactions = append(r.interactions, node)
	r.mu.Unlock()
	return resp, nil
}

// PrependFromFile loads an existing cassette (the `interactions:` format
// this recorder itself writes) and prepends its entries, so a recording run
// can extend a cassette across process restarts (e.g. turn 2 of a tool-call
// loop in a second invocation). Entries are kept as raw yaml.Nodes, so
// `!!binary` bodies round-trip byte-for-byte; the existing file's meta
// block is kept unless SetMeta was already called.
func (r *Recorder) PrependFromFile(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("recorder: read %s: %w", path, err)
	}
	var doc fileDoc
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("recorder: parse %s: %w", path, err)
	}
	if len(doc.Interactions) == 0 {
		return fmt.Errorf("recorder: %s has no interactions: list (not a cassette this tool wrote?)", path)
	}
	r.mu.Lock()
	r.interactions = append(doc.Interactions, r.interactions...)
	if r.meta == nil {
		r.meta = doc.Meta
	}
	r.mu.Unlock()
	return nil
}

// WriteFile serializes the meta block plus every recorded interaction to
// path (parent directories are created), in the exact shape cassette.Load
// parses.
func (r *Recorder) WriteFile(path string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.interactions) == 0 {
		return fmt.Errorf("recorder: no interactions recorded")
	}
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("recorder: mkdir %s: %w", dir, err)
		}
	}
	out, err := yaml.Marshal(fileDoc{Meta: r.meta, Interactions: r.interactions})
	if err != nil {
		return fmt.Errorf("recorder: marshal: %w", err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return fmt.Errorf("recorder: write %s: %w", path, err)
	}
	return nil
}

// =============================================================================
// on-disk shape (pytest-recording's interactions: format + meta block)
// =============================================================================

type fileDoc struct {
	Meta         *Meta       `yaml:"meta,omitempty"`
	Interactions []yaml.Node `yaml:"interactions"`
}

type interactionDoc struct {
	Request  requestDoc  `yaml:"request"`
	Response responseDoc `yaml:"response"`
}

type requestDoc struct {
	Body    any                 `yaml:"body"`
	Headers map[string][]string `yaml:"headers"`
	Method  string              `yaml:"method"`
	URI     string              `yaml:"uri"`
}

type responseDoc struct {
	Body    respBodyDoc         `yaml:"body"`
	Headers map[string][]string `yaml:"headers"`
	Status  statusDoc           `yaml:"status"`
}

type respBodyDoc struct {
	String any `yaml:"string"`
}

type statusDoc struct {
	Code    int    `yaml:"code"`
	Message string `yaml:"message"`
}

// bodyValue picks the YAML representation cassette.Load round-trips: a
// plain string for UTF-8 text (JSON, SSE), an explicitly-built !!binary
// scalar otherwise — yaml.Node.Encode would render a bare []byte held in an
// `any` field as a sequence of integers, not !!binary, so the scalar node
// is constructed by hand. nil for an empty body, matching how a bodyless
// request loads back as a nil slice.
func bodyValue(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	if utf8.Valid(b) {
		return string(b)
	}
	return &yaml.Node{
		Kind:  yaml.ScalarNode,
		Tag:   "!!binary",
		Value: base64.StdEncoding.EncodeToString(b),
	}
}

func (r *Recorder) scrubHeaders(h http.Header) map[string][]string {
	out := make(map[string][]string, len(h))
	for name, vals := range h {
		if r.scrubHeader[strings.ToLower(name)] {
			out[name] = []string{redacted}
			continue
		}
		cp := make([]string, len(vals))
		for i, v := range vals {
			cp[i] = r.redactString(v)
		}
		out[name] = cp
	}
	return out
}

func (r *Recorder) scrubURI(u *url.URL) string {
	cp := *u
	q := cp.Query()
	changed := false
	for name := range q {
		if r.scrubQuery[name] {
			q.Set(name, redacted)
			changed = true
		}
	}
	if changed {
		cp.RawQuery = q.Encode()
	}
	return r.redactString(cp.String())
}

func (r *Recorder) redactString(s string) string {
	for _, sec := range r.secretValues {
		s = strings.ReplaceAll(s, sec, redacted)
		// Query strings carry the key percent-/plus-escaped when it contains
		// reserved characters; cover the URL-escaped spelling too.
		if esc := url.QueryEscape(sec); esc != sec {
			s = strings.ReplaceAll(s, esc, redacted)
		}
	}
	return s
}

func statusMessage(resp *http.Response) string {
	return strings.TrimSpace(strings.TrimPrefix(resp.Status, strconv.Itoa(resp.StatusCode)))
}
