package scenario

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// Structural guarantees for the non-OpenAI-chat packs, in the same spirit
// as the openai-chat tests: each pins the protocol shapes that make the
// pack worth recording — deleting a shape's only carrier turns CI red
// instead of silently shrinking coverage.

func loadNamedPack(t *testing.T, dir string) *Pack {
	t.Helper()
	pack, err := LoadPack(dir)
	if err != nil {
		t.Fatalf("LoadPack(%s): %v", dir, err)
	}
	return pack
}

func TestAnthropicMessagesPack(t *testing.T) {
	pack := loadNamedPack(t, "../packs/anthropic-messages")
	if pack.Protocol != "anthropic" || pack.ModelField != "model" || pack.StreamField != "stream" {
		t.Fatalf("manifest: %+v", pack.Manifest)
	}

	var (
		hasStream, hasNoStream bool
		hasToolRoundTrip       bool // assistant tool_use blocks + user tool_result blocks
		hasParallelToolUse     bool
		hasThinking            bool
		hasStopSequences       bool
		hasPrefill             bool // conversation ending on an assistant turn
		hasStructuredOutput    bool
		hasStrictTool          bool
	)
	for _, sc := range pack.Scenarios {
		if sc.Stream {
			hasStream = true
		} else {
			hasNoStream = true
		}
		if gjson.GetBytes(sc.Body, "thinking.budget_tokens").Exists() {
			hasThinking = true
		}
		if gjson.GetBytes(sc.Body, "stop_sequences").IsArray() {
			hasStopSequences = true
		}
		if gjson.GetBytes(sc.Body, "output_config.format.type").String() == "json_schema" {
			hasStructuredOutput = true
		}
		gjson.GetBytes(sc.Body, "tools").ForEach(func(_, tool gjson.Result) bool {
			if tool.Get("strict").Bool() {
				hasStrictTool = true
			}
			return true
		})
		msgs := gjson.GetBytes(sc.Body, "messages").Array()
		if len(msgs) > 0 && msgs[len(msgs)-1].Get("role").String() == "assistant" {
			hasPrefill = true
		}
		sawToolUse, sawToolResult := 0, false
		for _, msg := range msgs {
			msg.Get("content").ForEach(func(_, block gjson.Result) bool {
				switch block.Get("type").String() {
				case "tool_use":
					sawToolUse++
				case "tool_result":
					sawToolResult = true
				}
				return true
			})
		}
		if sawToolUse > 0 && sawToolResult {
			hasToolRoundTrip = true
		}
		if sawToolUse >= 2 {
			hasParallelToolUse = true
		}
	}
	for name, ok := range map[string]bool{
		"a streaming scenario":              hasStream,
		"a non-streaming scenario":          hasNoStream,
		"a tool_use/tool_result round trip": hasToolRoundTrip,
		"parallel tool_use blocks":          hasParallelToolUse,
		"a thinking budget":                 hasThinking,
		"stop_sequences":                    hasStopSequences,
		"an assistant prefill":              hasPrefill,
		"a json_schema output_config":       hasStructuredOutput,
		"a strict tool definition":          hasStrictTool,
	} {
		if !ok {
			t.Errorf("anthropic-messages pack is missing %s", name)
		}
	}
}

func TestGeminiGenerateContentPack(t *testing.T) {
	pack := loadNamedPack(t, "../packs/gemini-generatecontent")
	if pack.Protocol != "gemini" || pack.ModelField != "" || pack.StreamField != "" {
		t.Fatalf("manifest: %+v", pack.Manifest)
	}

	var (
		hasFunctionDeclarations bool
		hasFunctionRoundTrip    bool // model function_call part + user function_response part
		hasResponseSchema       bool
		hasArraySchema          bool
		hasSafetySettings       bool
	)
	for _, sc := range pack.Scenarios {
		// The manifest says the model is not in the body — hold every
		// scenario to it, and to WithModel being a byte-identical no-op.
		if gjson.GetBytes(sc.Body, "model").Exists() {
			t.Errorf("%s: gemini bodies must not carry a model field", sc.Name)
		}
		if out, err := sc.WithModel("ignored"); err != nil || string(out) != string(sc.Body) {
			t.Errorf("%s: WithModel must be a no-op for URL-model packs", sc.Name)
		}
		if gjson.GetBytes(sc.Body, "safetySettings").IsArray() {
			hasSafetySettings = true
		}
		if gjson.GetBytes(sc.Body, "tools.0.functionDeclarations").IsArray() {
			hasFunctionDeclarations = true
		}
		schema := gjson.GetBytes(sc.Body, "generationConfig.response_schema")
		if schema.Exists() {
			hasResponseSchema = true
			schema.Get("properties").ForEach(func(_, prop gjson.Result) bool {
				if prop.Get("type").String() == "array" {
					hasArraySchema = true
				}
				return true
			})
		}
		sawCall, sawResponse := false, false
		gjson.GetBytes(sc.Body, "contents").ForEach(func(_, content gjson.Result) bool {
			content.Get("parts").ForEach(func(_, part gjson.Result) bool {
				if part.Get("function_call").Exists() {
					sawCall = true
				}
				if part.Get("function_response").Exists() {
					sawResponse = true
				}
				return true
			})
			return true
		})
		if sawCall && sawResponse {
			hasFunctionRoundTrip = true
		}
	}
	for name, ok := range map[string]bool{
		"functionDeclarations":                         hasFunctionDeclarations,
		"a function_call/function_response round trip": hasFunctionRoundTrip,
		"a generationConfig response_schema":           hasResponseSchema,
		"an array-typed response schema":               hasArraySchema,
		"safetySettings":                               hasSafetySettings,
	} {
		if !ok {
			t.Errorf("gemini-generatecontent pack is missing %s", name)
		}
	}
}

func TestOpenAIResponsesPack(t *testing.T) {
	pack := loadNamedPack(t, "../packs/openai-responses")
	if pack.Protocol != "openai-responses" || pack.ModelField != "model" || pack.StreamField != "stream" {
		t.Fatalf("manifest: %+v", pack.Manifest)
	}

	var (
		hasStream, hasNoStream bool
		hasInstructions        bool
		hasReasoning           bool
		hasTools               bool
		hasToolRoundTrip       bool // function_call + function_call_output input items
		hasStructuredOutput    bool
		hasStore               bool
	)
	for _, sc := range pack.Scenarios {
		if sc.Stream {
			hasStream = true
		} else {
			hasNoStream = true
		}
		if gjson.GetBytes(sc.Body, "instructions").Exists() {
			hasInstructions = true
		}
		if gjson.GetBytes(sc.Body, "reasoning.effort").Exists() {
			hasReasoning = true
		}
		if gjson.GetBytes(sc.Body, "store").Exists() {
			hasStore = true
		}
		if gjson.GetBytes(sc.Body, "tools.0.name").Exists() {
			hasTools = true
		}
		if gjson.GetBytes(sc.Body, "text.format.type").String() == "json_schema" {
			hasStructuredOutput = true
		}
		sawCall, sawOutput := false, false
		gjson.GetBytes(sc.Body, "input").ForEach(func(_, item gjson.Result) bool {
			switch item.Get("type").String() {
			case "function_call":
				sawCall = true
			case "function_call_output":
				sawOutput = true
			}
			return true
		})
		if sawCall && sawOutput {
			hasToolRoundTrip = true
		}
	}
	for name, ok := range map[string]bool{
		"a streaming scenario":                            hasStream,
		"a non-streaming scenario":                        hasNoStream,
		"an instructions field":                           hasInstructions,
		"a reasoning effort":                              hasReasoning,
		"a store flag":                                    hasStore,
		"a function tool":                                 hasTools,
		"a function_call/function_call_output round trip": hasToolRoundTrip,
		"a json_schema text.format":                       hasStructuredOutput,
	} {
		if !ok {
			t.Errorf("openai-responses pack is missing %s", name)
		}
	}
}

// TestManifestKeysMustBeExplicit: pack.json omitting model_field would
// silently mean "model rides in the URL" — WithModel becomes a no-op and
// every recording keeps the pack's committed model while the corpus path
// claims the vendor's. Missing keys must be a loud load error instead.
func TestManifestKeysMustBeExplicit(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "s.json"),
		[]byte(`{"model":"m","messages":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pack.json"),
		[]byte(`{"protocol":"openai","required":["model","messages"],"stream_field":"stream"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadPack(dir)
	if err == nil || !strings.Contains(err.Error(), "model_field") {
		t.Fatalf("missing model_field key accepted: %v", err)
	}
}

// TestOpenAIChatManifestMatchesDefaults: the openai-chat pack.json must
// stay equivalent to the no-manifest defaults old callers rely on.
func TestOpenAIChatManifestMatchesDefaults(t *testing.T) {
	pack := loadNamedPack(t, packDir)
	def := defaultManifest()
	if pack.Protocol != def.Protocol || pack.ModelField != def.ModelField || pack.StreamField != def.StreamField {
		t.Fatalf("openai-chat pack.json diverged from defaults: %+v", pack.Manifest)
	}
}
