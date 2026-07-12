package scenario

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/tidwall/gjson"
)

const packDir = "../packs/openai-chat"

func loadPack(t *testing.T) []Scenario {
	t.Helper()
	pack, err := LoadDir(packDir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	return pack
}

// TestPackCoversFullParameterMatrix enforces the pack's core guarantee:
// every top-level field the full-parameter scenario declares appears in at
// least one scenario, so batch-recording a new vendor exercises the same
// request-parameter surface a real SDK does — and deleting the scenario
// that was the only carrier of a field turns this red instead of silently
// shrinking coverage.
func TestPackCoversFullParameterMatrix(t *testing.T) {
	raw, err := os.ReadFile(packDir + "/chat_full_params.json")
	if err != nil {
		t.Fatalf("the pack must keep a chat_full_params.json full-matrix scenario: %v", err)
	}
	var full map[string]json.RawMessage
	if err := json.Unmarshal(raw, &full); err != nil {
		t.Fatalf("parse chat_full_params.json: %v", err)
	}

	covered := map[string]bool{}
	for _, sc := range loadPack(t) {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(sc.Body, &m); err != nil {
			t.Fatalf("%s: %v", sc.Name, err)
		}
		for k := range m {
			covered[k] = true
		}
	}
	for field := range full {
		if !covered[field] {
			t.Errorf("no scenario carries top-level field %q", field)
		}
	}
}

// TestPackStructuralGuarantees pins the shapes that can't be expressed as
// "field X exists": both stream buckets populated, a tool definition with a
// named tool_choice, a multi-turn tool-result round trip (assistant
// tool_calls + role:"tool" — the shape protocol translators most often get
// wrong), and a json_schema response_format.
func TestPackStructuralGuarantees(t *testing.T) {
	var (
		hasStream, hasNoStream    bool
		hasNamedToolChoice        bool
		hasToolRoundTrip          bool
		hasJSONSchemaFormat       bool
		hasParallelToolCallsInMsg bool
	)
	for _, sc := range loadPack(t) {
		if sc.Stream {
			hasStream = true
		} else {
			hasNoStream = true
		}
		if gjson.GetBytes(sc.Body, "tool_choice.function.name").Exists() {
			hasNamedToolChoice = true
		}
		if gjson.GetBytes(sc.Body, "response_format.json_schema").Exists() {
			hasJSONSchemaFormat = true
		}
		sawAssistantCalls, sawToolRole := false, false
		gjson.GetBytes(sc.Body, "messages").ForEach(func(_, msg gjson.Result) bool {
			if msg.Get("role").String() == "assistant" {
				if calls := msg.Get("tool_calls"); calls.IsArray() {
					sawAssistantCalls = true
					if len(calls.Array()) >= 2 {
						hasParallelToolCallsInMsg = true
					}
				}
			}
			if msg.Get("role").String() == "tool" {
				sawToolRole = true
			}
			return true
		})
		if sawAssistantCalls && sawToolRole {
			hasToolRoundTrip = true
		}
	}
	for name, ok := range map[string]bool{
		"a streaming scenario":                        hasStream,
		"a non-streaming scenario":                    hasNoStream,
		"a named tool_choice":                         hasNamedToolChoice,
		"a tool-result round trip":                    hasToolRoundTrip,
		"parallel tool_calls in the replayed history": hasParallelToolCallsInMsg,
		"a json_schema response_format":               hasJSONSchemaFormat,
	} {
		if !ok {
			t.Errorf("pack is missing %s", name)
		}
	}
}

// TestWithModel checks the one substitution batch recording performs.
func TestWithModel(t *testing.T) {
	for _, sc := range loadPack(t) {
		out, err := sc.WithModel("some-vendor-model")
		if err != nil {
			t.Fatalf("%s: %v", sc.Name, err)
		}
		if got := gjson.GetBytes(out, "model").String(); got != "some-vendor-model" {
			t.Errorf("%s: model = %q", sc.Name, got)
		}
		if gjson.GetBytes(out, "stream").Bool() != sc.Stream {
			t.Errorf("%s: stream flag changed by substitution", sc.Name)
		}
	}
}
