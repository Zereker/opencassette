// Package scenario loads the standard request-body packs under packs/ that
// the record command's batch mode replays against a real vendor API. A
// scenario is one JSON request body; the pack as a whole is what guarantees
// a recorded cassette set covers the same parameter surface a real SDK
// exercises, instead of whatever minimal body someone happened to hand-write
// on recording day — see packs/README.md for each body's provenance, and
// this package's tests for the coverage rules a pack must keep satisfying.
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

// Scenario is one request body from a pack.
type Scenario struct {
	Name   string // file basename without .json — becomes the cassette's scenario name
	Body   []byte
	Stream bool // the body's own "stream" field (drives the stream/nostream bucket)
}

// LoadDir reads every *.json file in dir (sorted by filename, deterministic
// batch order), validating that each parses and carries the two fields no
// chat request can go without.
func LoadDir(dir string) ([]Scenario, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("scenario: read %s: %w", dir, err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".json" {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, fmt.Errorf("scenario: no *.json scenarios in %s", dir)
	}

	out := make([]Scenario, 0, len(names))
	for _, name := range names {
		path := filepath.Join(dir, name)
		body, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("scenario: read %s: %w", path, err)
		}
		if !json.Valid(body) {
			return nil, fmt.Errorf("scenario: %s is not valid JSON", path)
		}
		if !gjson.GetBytes(body, "model").Exists() || !gjson.GetBytes(body, "messages").Exists() {
			return nil, fmt.Errorf("scenario: %s must have top-level model and messages fields", path)
		}
		out = append(out, Scenario{
			Name:   strings.TrimSuffix(name, ".json"),
			Body:   body,
			Stream: gjson.GetBytes(body, "stream").Bool(),
		})
	}
	return out, nil
}

// SHA256 returns the hex SHA-256 of the scenario body exactly as the pack
// file defines it — before the model substitution. Recorded into the
// cassette's meta block, it ties a capture to the precise pack version that
// produced it even after the pack file is later edited.
func (s Scenario) SHA256() string {
	sum := sha256.Sum256(s.Body)
	return hex.EncodeToString(sum[:])
}

// WithModel returns the scenario body with its "model" field replaced —
// the one per-vendor substitution batch recording makes; everything else
// is sent byte-for-byte as the pack defines it.
func (s Scenario) WithModel(model string) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(s.Body, &m); err != nil {
		return nil, fmt.Errorf("scenario %s: %w", s.Name, err)
	}
	quoted, err := json.Marshal(model)
	if err != nil {
		return nil, err
	}
	m["model"] = quoted
	return json.Marshal(m)
}
