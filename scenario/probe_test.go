package scenario

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/tidwall/gjson"
)

func buildPackProbes(t *testing.T) []Probe {
	t.Helper()
	baseRaw, err := os.ReadFile(packDir + "/chat_basic.json")
	if err != nil {
		t.Fatalf("probing needs the pack's chat_basic.json as base: %v", err)
	}
	base, err := Scenario{Name: "chat_basic", Body: baseRaw, ModelField: "model"}.WithModel("probe-model")
	if err != nil {
		t.Fatal(err)
	}
	fullRaw, err := os.ReadFile(packDir + "/chat_full_params.json")
	if err != nil {
		t.Fatalf("probing needs the pack's chat_full_params.json as field source: %v", err)
	}
	probes, err := BuildProbes(base, fullRaw)
	if err != nil {
		t.Fatalf("BuildProbes: %v", err)
	}
	return probes
}

// TestBuildProbesCoverEveryFullParamsField: one probe per non-core field,
// each a minimal valid body — base model/messages plus the field under
// test (and its declared companions), nothing else.
func TestBuildProbesCoverEveryFullParamsField(t *testing.T) {
	fullRaw, _ := os.ReadFile(packDir + "/chat_full_params.json")
	var full map[string]json.RawMessage
	if err := json.Unmarshal(fullRaw, &full); err != nil {
		t.Fatal(err)
	}

	probes := buildPackProbes(t)
	byField := map[string]Probe{}
	for i, p := range probes {
		byField[p.Field] = p
		if i > 0 && probes[i-1].Field >= p.Field {
			t.Errorf("probes not sorted: %q before %q", probes[i-1].Field, p.Field)
		}
		if !json.Valid(p.Body) {
			t.Errorf("%s: body not valid JSON", p.Field)
		}
		if got := gjson.GetBytes(p.Body, "model").String(); got != "probe-model" {
			t.Errorf("%s: model = %q", p.Field, got)
		}
		if !gjson.GetBytes(p.Body, "messages").Exists() {
			t.Errorf("%s: no messages", p.Field)
		}
		// nothing beyond base + field + companions
		var doc map[string]json.RawMessage
		_ = json.Unmarshal(p.Body, &doc)
		want := 3 + len(p.Companions)
		if len(doc) != want {
			t.Errorf("%s: body has %d fields, want %d (base + field + companions %v)", p.Field, len(doc), want, p.Companions)
		}
	}
	for field := range full {
		if field == "model" || field == "messages" {
			continue
		}
		if _, ok := byField[field]; !ok {
			t.Errorf("no probe generated for field %q", field)
		}
	}
	if _, ok := byField["model"]; ok {
		t.Error("model must not be probed")
	}
}

// TestBuildProbesRules pins the special cases: fields that need companions
// or a value override to actually exercise the capability.
func TestBuildProbesRules(t *testing.T) {
	byField := map[string]Probe{}
	for _, p := range buildPackProbes(t) {
		byField[p.Field] = p
	}

	for _, field := range []string{"tool_choice", "parallel_tool_calls"} {
		p := byField[field]
		if !gjson.GetBytes(p.Body, "tools").Exists() {
			t.Errorf("%s probe must carry tools: %s", field, p.Body)
		}
	}
	if p := byField["stream"]; !gjson.GetBytes(p.Body, "stream").Bool() || !p.Stream {
		t.Errorf("stream probe must send stream:true (committed false proves nothing): %s", p.Body)
	}
	if p := byField["audio"]; gjson.GetBytes(p.Body, "modalities").Raw != `["text","audio"]` {
		t.Errorf("audio probe must request the audio modality: %s", p.Body)
	}
	// stream_options can't live in chat_full_params (invalid with
	// stream:false) but must still be probed — it is the only way a
	// recorded stream carries usage/token accounting.
	if p, ok := byField["stream_options"]; !ok {
		t.Error("no synthetic stream_options probe generated")
	} else {
		if !gjson.GetBytes(p.Body, "stream_options.include_usage").Bool() {
			t.Errorf("stream_options probe must set include_usage: %s", p.Body)
		}
		if !gjson.GetBytes(p.Body, "stream").Bool() || !p.Stream {
			t.Errorf("stream_options probe must ride on stream:true: %s", p.Body)
		}
	}
	if p := byField["temperature"]; len(p.Companions) != 0 || p.Stream {
		t.Errorf("plain field grew companions/stream: %+v", p)
	}
}

// TestBuildProbesMissingCompanionErrors: dropping tools from full-params
// must break probe generation loudly, not emit an invalid tool_choice probe.
func TestBuildProbesMissingCompanionErrors(t *testing.T) {
	base := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	full := []byte(`{"model":"m","messages":[],"tool_choice":"auto"}`)
	if _, err := BuildProbes(base, full); err == nil {
		t.Fatal("tool_choice probe without tools in full-params must error")
	}
}
