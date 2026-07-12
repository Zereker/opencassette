// Package scenario loads the standard request-body packs under packs/ that
// the record command's batch mode replays against a real vendor API. A
// scenario is one JSON request body; the pack as a whole is what guarantees
// a recorded cassette set covers the same parameter surface a real SDK
// exercises, instead of whatever minimal body someone happened to hand-write
// on recording day — see packs/README.md for each body's provenance, and
// this package's tests for the coverage rules a pack must keep satisfying.
//
// Wire protocols differ in more than field names, so each pack carries a
// pack.json manifest declaring how its bodies work: which fields every
// scenario must have, where the model name lives (a body field for
// OpenAI/Anthropic; the URL path — no body field at all — for Gemini), and
// how streaming is signaled (a body field, or the endpoint itself for
// Gemini's :streamGenerateContent). A pack without a manifest gets the
// OpenAI-chat defaults, which is what every pack was before manifests
// existed.
package scenario

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tidwall/gjson"
)

// Manifest is a pack's pack.json: the protocol-specific facts the recorder
// needs to replay the pack's bodies correctly.
type Manifest struct {
	// Protocol is the corpus path segment (openai, anthropic, gemini, …)
	// recordings of this pack land under, unless -protocol overrides it.
	Protocol string `json:"protocol"`
	// Required lists top-level fields every scenario body must carry.
	Required []string `json:"required"`
	// ModelField is the body field holding the model name, substituted per
	// vendor at record time. Empty means the model is not in the body (it
	// rides in the URL — use a {model} placeholder in -url instead).
	ModelField string `json:"model_field"`
	// StreamField is the body field signaling a streaming request, driving
	// the stream/nostream corpus bucket. Empty means the body doesn't
	// decide (the endpoint does — record with an explicit -bucket).
	StreamField string `json:"stream_field"`
	// Spec optionally names the protocol's authoritative request schema,
	// for `opencassette audit` to diff the pack's coverage against. The
	// audit is one-way: the spec suggests fields the pack could grow to
	// carry; it never disqualifies recorded traffic.
	Spec *SpecRef `json:"spec,omitempty"`
}

// SpecRef locates an authoritative request schema.
type SpecRef struct {
	// Kind: "openapi" (URL is an OpenAPI document), "stainless-stats"
	// (URL is an SDK repo's .stats.yml whose openapi_spec_url names the
	// current spec — how OpenAI and Anthropic publish theirs), or
	// "discovery" (URL is a Google API discovery document).
	Kind string `json:"kind"`
	URL  string `json:"url"`
	// Path is the OpenAPI request path whose POST body schema to read
	// (openapi / stainless-stats kinds).
	Path string `json:"path,omitempty"`
	// Schema is the discovery-document schema name (discovery kind).
	Schema string `json:"schema,omitempty"`
}

func defaultManifest() Manifest {
	return Manifest{
		Protocol:    "openai",
		Required:    []string{"model", "messages"},
		ModelField:  "model",
		StreamField: "stream",
	}
}

// Pack is a loaded scenario pack: its manifest plus scenarios in filename
// order (deterministic batch order).
type Pack struct {
	Manifest
	Scenarios []Scenario
}

// Scenario is one request body from a pack.
type Scenario struct {
	Name string // file basename without .json — becomes the cassette's scenario name
	Body []byte
	// ModelField is inherited from the pack manifest; WithModel substitutes
	// this field ("" = the body carries no model, WithModel is a no-op).
	ModelField string
	Stream     bool // manifest's stream_field in the body (false when the manifest declares none)
}

// LoadPack reads a pack directory: pack.json if present (OpenAI-chat
// defaults otherwise), then every other *.json file sorted by name, each
// validated against the manifest's required fields.
func LoadPack(dir string) (*Pack, error) {
	man := defaultManifest()

	if raw, err := os.ReadFile(filepath.Join(dir, "pack.json")); err == nil {
		// An empty model_field/stream_field is meaningful ("not in the
		// body"), so a silently-missing key would flip semantics — every
		// key must be written out explicitly.
		var keys map[string]json.RawMessage
		if err := json.Unmarshal(raw, &keys); err != nil {
			return nil, fmt.Errorf("scenario: parse %s/pack.json: %w", dir, err)
		}

		for _, k := range []string{"protocol", "required", "model_field", "stream_field"} {
			if _, ok := keys[k]; !ok {
				return nil, fmt.Errorf(`scenario: %s/pack.json must set %q explicitly ("" means "not in the body")`, dir, k)
			}
		}

		man = Manifest{}
		if err := json.Unmarshal(raw, &man); err != nil {
			return nil, fmt.Errorf("scenario: parse %s/pack.json: %w", dir, err)
		}

		if man.Protocol == "" || len(man.Required) == 0 {
			return nil, fmt.Errorf("scenario: %s/pack.json must declare protocol and required fields", dir)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("scenario: read %s/pack.json: %w", dir, err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("scenario: read %s: %w", dir, err)
	}

	var names []string

	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".json" && e.Name() != "pack.json" {
			names = append(names, e.Name())
		}
	}

	sort.Strings(names)

	if len(names) == 0 {
		return nil, fmt.Errorf("scenario: no *.json scenarios in %s", dir)
	}

	pack := &Pack{Manifest: man, Scenarios: make([]Scenario, 0, len(names))}
	for _, name := range names {
		path := filepath.Join(dir, name)

		body, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("scenario: read %s: %w", path, err)
		}

		if !json.Valid(body) {
			return nil, fmt.Errorf("scenario: %s is not valid JSON", path)
		}

		for _, field := range man.Required {
			if !gjson.GetBytes(body, field).Exists() {
				return nil, fmt.Errorf("scenario: %s must have top-level %q (required by pack.json)", path, field)
			}
		}

		stream := false
		if man.StreamField != "" {
			stream = gjson.GetBytes(body, man.StreamField).Bool()
		}

		pack.Scenarios = append(pack.Scenarios, Scenario{
			Name:       strings.TrimSuffix(name, ".json"),
			Body:       body,
			ModelField: man.ModelField,
			Stream:     stream,
		})
	}

	return pack, nil
}

// LoadDir is LoadPack without the manifest — kept for callers that only
// need the scenario list.
func LoadDir(dir string) ([]Scenario, error) {
	pack, err := LoadPack(dir)
	if err != nil {
		return nil, err
	}

	return pack.Scenarios, nil
}

// SHA256 returns the hex SHA-256 of the scenario body exactly as the pack
// file defines it — before the model substitution. Recorded into the
// cassette's meta block, it ties a capture to the precise pack version that
// produced it even after the pack file is later edited.
func (s Scenario) SHA256() string {
	sum := sha256.Sum256(s.Body)
	return hex.EncodeToString(sum[:])
}

// WithModel returns the scenario body with the manifest-declared model
// field replaced — the one per-vendor substitution batch recording makes;
// everything else is sent byte-for-byte as the pack defines it. For packs
// whose model rides in the URL (empty ModelField) the body is returned
// unchanged.
func (s Scenario) WithModel(model string) ([]byte, error) {
	if s.ModelField == "" {
		return s.Body, nil
	}

	var m map[string]json.RawMessage
	if err := json.Unmarshal(s.Body, &m); err != nil {
		return nil, fmt.Errorf("scenario %s: %w", s.Name, err)
	}

	quoted, err := json.Marshal(model)
	if err != nil {
		return nil, err
	}

	m[s.ModelField] = quoted

	return json.Marshal(m)
}
