package redact

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Profile is a vendor's redaction overlay: correlation and secret carriers a
// vendor emits that the cross-vendor Baseline does not know about. It is
// plain data (profiles/<vendor>.yaml), so covering a newly observed header
// is a one-line edit that needs no recompile.
type Profile struct {
	// CredentialHeaders are blanked to **REDACTED**.
	CredentialHeaders []string `yaml:"credential_headers"`
	// TraceHeaders are discovered and rewritten to **TRACE_ID_n** markers,
	// consistently across headers, URI and bodies.
	TraceHeaders []string `yaml:"trace_headers"`
	// QueryParams are blanked to **REDACTED** in the request URI.
	QueryParams []string `yaml:"query_params"`
	// Replacements are custom substitutions applied to the recorded copy
	// after the standard scrubbing above.
	Replacements []Replacement `yaml:"replacements"`
}

// Replacement is a custom substitution on the recorded copy. Exactly one of
// Header, Find or Pattern is set:
//
//   - Header replaces a whole header value by name (case-insensitive).
//   - Find substitutes a literal string.
//   - Pattern substitutes a regexp match.
//
// With is the replacement text. In limits the scope to any of "header",
// "uri", "body"; empty means all three. Header rules ignore In (they always
// target the named header).
type Replacement struct {
	Header  string   `yaml:"header,omitempty"`
	Find    string   `yaml:"find,omitempty"`
	Pattern string   `yaml:"pattern,omitempty"`
	With    string   `yaml:"with"`
	In      []string `yaml:"in,omitempty"`
}

// LoadProfile reads dir/<vendor>.yaml. A missing file is not an error — most
// vendors need nothing beyond the Baseline — so it returns (nil, nil), which
// Rules.Merge treats as a no-op. An empty dir or vendor also yields no
// profile.
func LoadProfile(dir, vendor string) (*Profile, error) {
	if dir == "" || vendor == "" {
		return nil, nil
	}

	path := filepath.Join(dir, vendor+".yaml")

	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("redact: read profile %s: %w", path, err)
	}

	var p Profile
	if err := yaml.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("redact: parse profile %s: %w", path, err)
	}

	return &p, nil
}
