// Package audit diffs a scenario pack's request-field coverage against the
// protocol's authoritative schema — the ceiling the pack should grow
// toward. The direction matters: the spec suggests fields worth recording
// next; it is never used to validate or reject recorded traffic, because
// what vendors actually accept on the wire (including their deviations
// from the spec) is exactly the data this project exists to capture.
//
// Supported spec sources (a pack's pack.json "spec" block):
//
//   - openapi: a plain OpenAPI YAML/JSON document; fields come from the
//     POST request body schema at the given path, following $ref/allOf.
//   - stainless-stats: an SDK repo's .stats.yml whose openapi_spec_url
//     names the current spec — how OpenAI and Anthropic publish theirs
//     (the SDKs are generated from these documents).
//   - discovery: a Google API discovery document; fields come from the
//     named schema's properties (Gemini's GenerateContentRequest).
package audit

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/zereker/opencassette/scenario"
)

// Fields fetches ref and returns the sorted top-level request-body field
// names it declares.
func Fields(client *http.Client, ref *scenario.SpecRef) ([]string, error) {
	if ref == nil {
		return nil, fmt.Errorf("audit: pack.json declares no spec")
	}

	switch ref.Kind {
	case "openapi":
		raw, err := fetch(client, ref.URL)
		if err != nil {
			return nil, err
		}

		return openapiRequestFields(raw, ref.Path)
	case "stainless-stats":
		specURL, err := StainlessSpecURL(client, ref.URL)
		if err != nil {
			return nil, err
		}

		spec, err := fetch(client, specURL)
		if err != nil {
			return nil, err
		}

		return openapiRequestFields(spec, ref.Path)
	case "discovery":
		raw, err := fetch(client, ref.URL)
		if err != nil {
			return nil, err
		}

		return discoveryFields(raw, ref.Schema)
	default:
		return nil, fmt.Errorf("audit: unknown spec kind %q (want openapi | stainless-stats | discovery)", ref.Kind)
	}
}

// StainlessSpecURL resolves an SDK repo's .stats.yml to the OpenAPI spec
// URL it currently names. The URLs are content-addressed (a hash in the
// filename), so the result is also what a maintainer pins in pack.json —
// swap the pack's spec to kind "openapi" with this URL to make audits
// deterministic, and bumping the ceiling becomes an explicit one-line
// change.
func StainlessSpecURL(client *http.Client, statsURL string) (string, error) {
	raw, err := fetch(client, statsURL)
	if err != nil {
		return "", err
	}

	var stats struct {
		OpenAPISpecURL string `yaml:"openapi_spec_url"`
	}
	if err := yaml.Unmarshal(raw, &stats); err != nil || stats.OpenAPISpecURL == "" {
		return "", fmt.Errorf("audit: %s has no openapi_spec_url (not a Stainless .stats.yml?)", statsURL)
	}

	return stats.OpenAPISpecURL, nil
}

// Report is the pack-vs-spec diff.
type Report struct {
	SpecTotal int
	Covered   []string // in both
	Missing   []string // spec-only: candidates for the pack to grow
	Extra     []string // pack-only: vendor extensions, or spec drift
}

// Compare diffs the pack's field union against the spec's field list.
func Compare(packFields, specFields []string) Report {
	pack := toSet(packFields)
	spec := toSet(specFields)

	r := Report{SpecTotal: len(spec)}
	for f := range spec {
		if pack[f] {
			r.Covered = append(r.Covered, f)
		} else {
			r.Missing = append(r.Missing, f)
		}
	}

	for f := range pack {
		if !spec[f] {
			r.Extra = append(r.Extra, f)
		}
	}

	sort.Strings(r.Covered)
	sort.Strings(r.Missing)
	sort.Strings(r.Extra)

	return r
}

// PackFields returns the union of top-level fields across a pack's
// scenario bodies, plus the synthetic probe fields for the OpenAI chat
// protocol (probing covers them even though no body can carry them). For
// packs whose model rides in the URL, "model" is excluded from audits by
// the caller dropping it from the spec side — the pack not carrying it is
// by design, not a gap.
func PackFields(pack *scenario.Pack) []string {
	set := map[string]bool{}

	for _, sc := range pack.Scenarios {
		var doc map[string]json.RawMessage
		if json.Unmarshal(sc.Body, &doc) != nil {
			continue // LoadPack already validated; belt and braces
		}

		for f := range doc {
			set[f] = true
		}
	}

	if pack.Protocol == "openai" {
		for _, f := range scenario.SyntheticProbeFields() {
			set[f] = true
		}
	}

	out := make([]string, 0, len(set))
	for f := range set {
		out = append(out, f)
	}

	sort.Strings(out)

	return out
}

func fetch(client *http.Client, url string) ([]byte, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("audit: fetch %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("audit: fetch %s: HTTP %s", url, resp.Status)
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("audit: read %s: %w", url, err)
	}

	return raw, nil
}

// openapiRequestFields walks paths[path].post.requestBody's JSON schema,
// following $ref and merging allOf, and returns its top-level properties.
func openapiRequestFields(raw []byte, path string) ([]string, error) {
	var spec map[string]any
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		return nil, fmt.Errorf("audit: parse OpenAPI document: %w", err)
	}

	paths, _ := spec["paths"].(map[string]any)

	p, ok := paths[path].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("audit: path %q not found in spec", path)
	}

	schema := dig(p, "post", "requestBody", "content", "application/json", "schema")
	if schema == nil {
		return nil, fmt.Errorf("audit: no JSON request schema at POST %s", path)
	}

	set := map[string]bool{}
	collectProps(spec, schema, set, 0)

	if len(set) == 0 {
		return nil, fmt.Errorf("audit: request schema at POST %s has no resolvable properties", path)
	}

	out := make([]string, 0, len(set))
	for f := range set {
		out = append(out, f)
	}

	sort.Strings(out)

	return out, nil
}

// collectProps gathers property names from a schema node, resolving local
// $ref pointers and merging allOf members. Depth-capped: specs are graphs
// and a cyclic $ref must not hang the audit.
func collectProps(spec map[string]any, node any, out map[string]bool, depth int) {
	if depth > 8 {
		return
	}

	m, ok := node.(map[string]any)
	if !ok {
		return
	}

	if ref, ok := m["$ref"].(string); ok {
		collectProps(spec, deref(spec, ref), out, depth+1)
		return
	}

	if all, ok := m["allOf"].([]any); ok {
		for _, sub := range all {
			collectProps(spec, sub, out, depth+1)
		}
	}

	if props, ok := m["properties"].(map[string]any); ok {
		for f := range props {
			out[f] = true
		}
	}
}

func deref(spec map[string]any, ref string) any {
	if !strings.HasPrefix(ref, "#/") {
		return nil // remote refs are out of scope
	}

	var cur any = spec
	for _, seg := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}

		cur = m[seg]
	}

	return cur
}

func discoveryFields(raw []byte, schema string) ([]string, error) {
	var doc struct {
		Schemas map[string]struct {
			Properties map[string]any `json:"properties"`
		} `json:"schemas"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("audit: parse discovery document: %w", err)
	}

	s, ok := doc.Schemas[schema]
	if !ok || len(s.Properties) == 0 {
		return nil, fmt.Errorf("audit: schema %q not found in discovery document", schema)
	}

	out := make([]string, 0, len(s.Properties))
	for f := range s.Properties {
		out = append(out, f)
	}

	sort.Strings(out)

	return out, nil
}

func dig(m map[string]any, keys ...string) any {
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}

		cur = mm[k]
	}

	return cur
}

func toSet(list []string) map[string]bool {
	set := make(map[string]bool, len(list))
	for _, f := range list {
		set[f] = true
	}

	return set
}
