package scenario

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/tidwall/gjson"
)

// A Probe is one generated per-field request: the minimal base body plus a
// single top-level field under test. Sending each field in isolation is
// what turns a recording run into per-field evidence of vendor support —
// the monolithic full-params scenario can only prove "this exact bundle
// was accepted", and a strict vendor's 4xx on the bundle says nothing
// about which fields it does support.
type Probe struct {
	Field      string
	Body       []byte
	Stream     bool
	Companions []string // fields carried alongside because Field is invalid without them
}

// probeRule adjusts a field's probe when sending it verbatim and alone
// would measure request-validation noise instead of vendor support.
type probeRule struct {
	value      json.RawMessage            // overrides the full-params value; nil = as committed
	companions []string                   // copied from the full-params body
	extra      map[string]json.RawMessage // fixed extra fields
}

var probeRules = map[string]probeRule{
	// tool_choice / parallel_tool_calls are only meaningful (and only
	// accepted) when tools are declared in the same request.
	"tool_choice":         {companions: []string{"tools"}},
	"parallel_tool_calls": {companions: []string{"tools"}},
	// The committed value is false, which every vendor treats as absent;
	// proving streaming support requires actually asking for a stream.
	"stream": {value: json.RawMessage(`true`)},
	// Audio output is rejected unless the audio modality is requested.
	"audio": {extra: map[string]json.RawMessage{"modalities": json.RawMessage(`["text","audio"]`)}},
}

// syntheticProbes are fields probed even though the full-params body can't
// carry them: stream_options is rejected outright when stream is false, so
// putting it in chat_full_params.json would invalidate that scenario. It
// still needs per-field evidence — include_usage is the only way a stream
// carries token accounting at all, and a stream probe without it records a
// cassette with no usage data. The value mirrors the pack's
// chat_stream_usage.json (a verbatim real SDK request).
var syntheticProbes = map[string]probeRule{
	"stream_options": {
		value: json.RawMessage(`{"include_usage":true}`),
		extra: map[string]json.RawMessage{"stream": json.RawMessage(`true`)},
	},
}

// BuildProbes derives one probe per top-level field of fullParams (except
// model and messages, which come from base — the pack's minimal scenario,
// already model-substituted), plus the synthetic probes for fields the
// full-params body cannot legally carry. Probes are returned sorted by
// field name and their bodies marshal deterministically.
func BuildProbes(base, fullParams []byte) ([]Probe, error) {
	var baseDoc, fullDoc map[string]json.RawMessage
	if err := json.Unmarshal(base, &baseDoc); err != nil {
		return nil, fmt.Errorf("scenario: parse probe base: %w", err)
	}
	if err := json.Unmarshal(fullParams, &fullDoc); err != nil {
		return nil, fmt.Errorf("scenario: parse full-params: %w", err)
	}
	model, okM := baseDoc["model"]
	messages, okMsg := baseDoc["messages"]
	if !okM || !okMsg {
		return nil, fmt.Errorf("scenario: probe base must have model and messages")
	}

	var fields []string
	for f := range fullDoc {
		if f != "model" && f != "messages" {
			fields = append(fields, f)
		}
	}
	if len(fields) == 0 {
		return nil, fmt.Errorf("scenario: full-params has no probeable fields")
	}
	for f := range syntheticProbes {
		if _, ok := fullDoc[f]; !ok {
			fields = append(fields, f)
		}
	}
	sort.Strings(fields)

	out := make([]Probe, 0, len(fields))
	for _, field := range fields {
		val, inFull := fullDoc[field]
		rule := probeRules[field]
		if !inFull {
			rule = syntheticProbes[field]
		}
		doc := map[string]json.RawMessage{"model": model, "messages": messages}
		if rule.value != nil {
			val = rule.value
		}
		doc[field] = val
		var companions []string
		for _, c := range rule.companions {
			cv, ok := fullDoc[c]
			if !ok {
				return nil, fmt.Errorf("scenario: full-params no longer carries %q, required by the %q probe", c, field)
			}
			doc[c] = cv
			companions = append(companions, c)
		}
		for k, v := range rule.extra {
			doc[k] = v
			companions = append(companions, k)
		}
		sort.Strings(companions)
		body, err := json.Marshal(doc)
		if err != nil {
			return nil, fmt.Errorf("scenario: marshal %q probe: %w", field, err)
		}
		out = append(out, Probe{
			Field:      field,
			Body:       body,
			Stream:     gjson.GetBytes(body, "stream").Bool(),
			Companions: companions,
		})
	}
	return out, nil
}
