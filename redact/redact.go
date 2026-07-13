// Package redact turns a recorded HTTP exchange into a publishable one. It
// blanks credential-bearing headers and query parameters, replaces literal
// secret values wherever their bytes appear, and rewrites
// trace/correlation identifiers to stable **TRACE_ID_n** markers
// consistently across every header, URI and body of a recording.
//
// Rules are declarative and layered. Baseline carries the cross-vendor set
// every recording applies; a vendor Profile (a YAML data file, see
// profile.go) overlays vendor-specific carriers. Covering a newly observed
// header is therefore a one-line data change, not a code change — which is
// the whole reason this logic lives in its own package instead of being
// hard-coded inside the recorder.
package redact

import (
	_ "embed"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// Replacement scopes: which part of the recorded copy a find/pattern rule
// applies to. An empty scope set means all three.
const (
	ScopeHeader = "header"
	ScopeURI    = "uri"
	ScopeBody   = "body"
)

const (
	// Redacted replaces a credential header value or a literal secret.
	Redacted = "**REDACTED**"
	// TraceRedacted marks a trace header whose value was too short to
	// safely propagate into bodies (see traceParts): the header is blanked
	// by name, but the value is not substituted globally.
	TraceRedacted = "**TRACE_ID**"
)

// baselineYAML is the cross-vendor baseline as data. Embedding keeps it
// compiled into the binary — the credential and trace fail-safe works
// offline, with no external file — while making the rule set reviewable and
// extendable as data rather than a hard-coded map.
//
//go:embed baseline.yaml
var baselineYAML []byte

// baselineProfile is baselineYAML parsed once at init. A corrupt embed is a
// build-time bug, so parsing panics rather than returning an error.
var baselineProfile = mustParseBaseline()

func mustParseBaseline() *Profile {
	var p Profile
	if err := yaml.Unmarshal(baselineYAML, &p); err != nil {
		panic("redact: invalid embedded baseline.yaml: " + err.Error())
	}

	return &p
}

// Rules is the declarative redaction policy: which header and query names to
// blank, which header values to treat as correlation carriers, plus literal
// secret values to replace wherever they appear. Configure it before
// recording starts — the setters are not synchronized against an in-flight
// Scrubber.
type Rules struct {
	credentialHeaders map[string]bool
	traceHeaders      map[string]bool
	queryParams       map[string]bool
	secrets           []string
	headerOverrides   map[string]string
	replacements      []compiledReplacement
}

// compiledReplacement is a validated custom replacement: either a literal
// find or a compiled regexp, substituted with with within the given scopes
// (nil scopes = every scope). Applied to the recorded copy only, after the
// standard secret/trace scrubbing.
type compiledReplacement struct {
	find   string
	re     *regexp.Regexp
	with   string
	scopes map[string]bool
}

// Baseline returns the built-in cross-vendor ruleset, parsed from the
// embedded baseline.yaml. Callers overlay vendor-specific carriers with
// Merge before recording.
func Baseline() *Rules {
	r := &Rules{
		credentialHeaders: map[string]bool{},
		traceHeaders:      map[string]bool{},
		queryParams:       map[string]bool{},
		headerOverrides:   map[string]string{},
	}

	if err := r.Merge(baselineProfile); err != nil {
		panic("redact: invalid embedded baseline.yaml: " + err.Error())
	}

	return r
}

// AddCredentialHeader adds a header name (case-insensitive) whose value is
// blanked to **REDACTED**.
func (r *Rules) AddCredentialHeader(name string) { r.credentialHeaders[strings.ToLower(name)] = true }

// AddTraceHeader adds a header name (case-insensitive) whose value is
// discovered and consistently rewritten to a **TRACE_ID_n** marker.
func (r *Rules) AddTraceHeader(name string) { r.traceHeaders[strings.ToLower(name)] = true }

// AddQueryParam adds a query-parameter name whose value is blanked (for
// key-in-URL auth styles like Gemini AI Studio's ?key=...).
func (r *Rules) AddQueryParam(name string) { r.queryParams[name] = true }

// AddSecret registers a literal secret (an API key) to be replaced wherever
// its exact bytes appear, regardless of which header or parameter carried it.
func (r *Rules) AddSecret(v string) {
	if v != "" {
		r.secrets = append(r.secrets, v)
	}
}

// Merge overlays a vendor Profile's carriers onto the ruleset. A nil profile
// is a no-op, so callers need not branch on "this vendor has no profile". It
// returns an error if a custom replacement is malformed (e.g. an invalid
// regexp), so a bad profile fails at load time rather than silently.
func (r *Rules) Merge(p *Profile) error {
	if p == nil {
		return nil
	}

	for _, h := range p.CredentialHeaders {
		r.AddCredentialHeader(h)
	}

	for _, h := range p.TraceHeaders {
		r.AddTraceHeader(h)
	}

	for _, q := range p.QueryParams {
		r.AddQueryParam(q)
	}

	for _, rp := range p.Replacements {
		if err := r.AddReplacement(rp); err != nil {
			return err
		}
	}

	return nil
}

// AddReplacement registers a custom replacement. Exactly one of Header, Find
// or Pattern must be set: Header replaces a whole header value by name, Find
// substitutes a literal string, Pattern substitutes a regexp match. With is
// the replacement text; In limits the scope (empty = header, URI and body).
func (r *Rules) AddReplacement(rp Replacement) error {
	if rp.With == "" {
		return fmt.Errorf("redact: replacement needs a 'with' value")
	}

	set := 0
	for _, v := range []string{rp.Header, rp.Find, rp.Pattern} {
		if v != "" {
			set++
		}
	}

	if set != 1 {
		return fmt.Errorf("redact: replacement needs exactly one of header/find/pattern")
	}

	if rp.Header != "" {
		r.headerOverrides[strings.ToLower(rp.Header)] = rp.With

		return nil
	}

	c := compiledReplacement{with: rp.With, scopes: scopeSet(rp.In)}

	if rp.Pattern != "" {
		re, err := regexp.Compile(rp.Pattern)
		if err != nil {
			return fmt.Errorf("redact: invalid pattern %q: %w", rp.Pattern, err)
		}

		c.re = re
	} else {
		c.find = rp.Find
	}

	r.replacements = append(r.replacements, c)

	return nil
}

// scopeSet lowercases an In list into a lookup set; nil (all scopes) for an
// empty list.
func scopeSet(in []string) map[string]bool {
	if len(in) == 0 {
		return nil
	}

	m := make(map[string]bool, len(in))
	for _, s := range in {
		m[strings.ToLower(s)] = true
	}

	return m
}

// Scrubber applies a Rules set to one recording. Trace tokens are stable for
// its lifetime, so a value repeated across headers, JSON and SSE chunks —
// even across separate exchanges — maps to the same **TRACE_ID_n** marker.
type Scrubber struct {
	rules *Rules

	mu          sync.Mutex
	traceValues map[string]string
	nextTrace   int
}

// NewScrubber builds a Scrubber backed by these rules. The rules are shared
// by reference, so late AddSecret calls (registering the live key after
// construction) still take effect.
func (r *Rules) NewScrubber() *Scrubber {
	return &Scrubber{rules: r, traceValues: map[string]string{}}
}

// AddSecret registers a literal secret after the Scrubber is built, e.g. the
// API key resolved just before the first request.
func (s *Scrubber) AddSecret(v string) { s.rules.AddSecret(v) }

// AddCredentialHeader adds a credential header name after construction.
func (s *Scrubber) AddCredentialHeader(name string) { s.rules.AddCredentialHeader(name) }

// AddQueryParam adds a query-parameter name after construction.
func (s *Scrubber) AddQueryParam(name string) { s.rules.AddQueryParam(name) }

// Headers returns a scrubbed copy of h: credential headers blanked, trace
// headers rewritten to markers, every other value passed through the same
// secret/trace substitution so an echoed key or trace ID cannot leak.
func (s *Scrubber) Headers(h http.Header, traceReplacements map[string]string) map[string][]string {
	out := make(map[string][]string, len(h))

	for name, vals := range h {
		if s.rules.credentialHeaders[strings.ToLower(name)] {
			out[name] = []string{Redacted}

			continue
		}

		if with, ok := s.rules.headerOverrides[strings.ToLower(name)]; ok {
			out[name] = []string{with}

			continue
		}

		if s.rules.traceHeaders[strings.ToLower(name)] {
			redactedValues := make([]string, len(vals))
			for i, v := range vals {
				redactedValues[i] = s.redactString(v, traceReplacements, false)
				if redactedValues[i] == v {
					redactedValues[i] = TraceRedacted
				}
			}

			out[name] = redactedValues

			continue
		}

		cp := make([]string, len(vals))
		for i, v := range vals {
			cp[i] = s.applyReplacements(s.redactString(v, traceReplacements, false), ScopeHeader)
		}

		out[name] = cp
	}

	return out
}

// URI returns u as a scrubbed string: configured query parameters blanked,
// then secret/trace substitution applied (covering URL-escaped spellings).
func (s *Scrubber) URI(u *url.URL, traceReplacements map[string]string) string {
	cp := *u
	q := cp.Query()
	changed := false

	for name := range q {
		if s.rules.queryParams[name] {
			q.Set(name, Redacted)

			changed = true
		}
	}

	if changed {
		cp.RawQuery = q.Encode()
	}

	return s.applyReplacements(s.redactString(cp.String(), traceReplacements, true), ScopeURI)
}

// Bytes returns a scrubbed copy of a request or response body.
func (s *Scrubber) Bytes(b []byte, traceReplacements map[string]string) []byte {
	if len(b) == 0 {
		return b
	}

	return []byte(s.applyReplacements(s.redactString(string(b), traceReplacements, false), ScopeBody))
}

// applyReplacements runs the custom replacements whose scope includes this
// part of the recorded copy, after standard secret/trace scrubbing. It
// touches only the string being written to disk, never the live exchange.
func (s *Scrubber) applyReplacements(str, scope string) string {
	for _, rp := range s.rules.replacements {
		if rp.scopes != nil && !rp.scopes[scope] {
			continue
		}

		if rp.re != nil {
			str = rp.re.ReplaceAllString(str, rp.with)
		} else {
			str = strings.ReplaceAll(str, rp.find, rp.with)
		}
	}

	return str
}

func (s *Scrubber) redactString(str string, traceReplacements map[string]string, escaped bool) string {
	// Replace longer values first. Composite formats such as traceparent also
	// contribute their trace/span components, and replacing a component first
	// would prevent the full carrier from being recognized.
	keys := make([]string, 0, len(traceReplacements))
	for value := range traceReplacements {
		keys = append(keys, value)
	}

	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })

	for _, value := range keys {
		str = strings.ReplaceAll(str, value, traceReplacements[value])
		if escaped {
			if esc := url.QueryEscape(value); esc != value {
				str = strings.ReplaceAll(str, esc, traceReplacements[value])
			}
		}
	}

	for _, sec := range s.rules.secrets {
		str = strings.ReplaceAll(str, sec, Redacted)
		// Query strings carry the key percent-/plus-escaped when it contains
		// reserved characters; cover the URL-escaped spelling too.
		if escaped {
			if esc := url.QueryEscape(sec); esc != sec {
				str = strings.ReplaceAll(str, esc, Redacted)
			}
		}
	}

	return str
}

// TraceReplacements discovers trace/correlation values across the given
// headers and maps each to a stable marker. Pass the request and response
// headers of one exchange; the returned map feeds Headers, URI and Bytes so
// the same identifier is rewritten identically everywhere it appears.
func (s *Scrubber) TraceReplacements(headers ...http.Header) map[string]string {
	values := map[string]bool{}

	for _, h := range headers {
		for name, items := range h {
			if !s.rules.traceHeaders[strings.ToLower(name)] {
				continue
			}

			for _, item := range items {
				for _, value := range traceParts(name, item) {
					if value != "" {
						values[value] = true
					}
				}
			}
		}
	}

	ordered := make([]string, 0, len(values))
	for value := range values {
		ordered = append(ordered, value)
	}

	sort.Strings(ordered)

	out := make(map[string]string, len(ordered))
	for _, value := range ordered {
		out[value] = s.traceToken(value)
	}

	return out
}

func (s *Scrubber) traceToken(value string) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if token := s.traceValues[value]; token != "" {
		return token
	}

	s.nextTrace++
	token := traceMarker(s.nextTrace)
	s.traceValues[value] = token

	return token
}

func traceMarker(n int) string {
	return "**TRACE_ID_" + strconv.Itoa(n) + "**"
}

// traceParts returns both the full carrier and independently useful trace or
// span components that may also appear in a JSON/SSE response body.
func traceParts(name, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}

	// Very short request IDs are not safe global replacements: replacing an
	// ID such as "1" throughout a JSON response would corrupt unrelated data.
	// The header itself is still scrubbed by name; only sufficiently specific
	// values are propagated into bodies and URIs.
	parts := make([]string, 0, 3)
	appendPart := func(part string) {
		if len(part) >= 8 {
			parts = append(parts, part)
		}
	}
	appendPart(value)

	switch strings.ToLower(name) {
	case "b3":
		fields := strings.Split(value, "-")
		if len(fields) >= 2 {
			appendPart(fields[0])
			appendPart(fields[1])
		}
	case "traceparent":
		fields := strings.Split(value, "-")
		if len(fields) >= 4 {
			appendPart(fields[1])
			appendPart(fields[2])
		}
	case "uber-trace-id":
		fields := strings.Split(value, ":")
		if len(fields) >= 2 {
			appendPart(fields[0])
			appendPart(fields[1])
		}
	case "x-cloud-trace-context":
		traceID, rest, ok := strings.Cut(value, "/")
		if ok {
			appendPart(traceID)

			spanID, _, _ := strings.Cut(rest, ";")
			appendPart(spanID)
		}
	case "x-amzn-trace-id":
		for field := range strings.SplitSeq(value, ";") {
			key, component, ok := strings.Cut(strings.TrimSpace(field), "=")
			if ok && (strings.EqualFold(key, "root") || strings.EqualFold(key, "parent")) {
				appendPart(component)
			}
		}
	}

	return parts
}
