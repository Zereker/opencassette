package cli

import (
	"encoding/json"
	"testing"
)

// TestRewriteBedrockBody checks the Bedrock body transform: model and stream
// are dropped (the model is the URL ARN), anthropic_version is injected, and
// the rest of the payload survives.
func TestRewriteBedrockBody(t *testing.T) {
	in := []byte(`{"model":"claude","stream":true,"messages":[{"role":"user"}],"max_tokens":16}`)

	out, err := rewriteBedrockBody(in)
	if err != nil {
		t.Fatal(err)
	}

	var m map[string]json.RawMessage
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}

	if _, ok := m["model"]; ok {
		t.Error("model was not stripped")
	}

	if _, ok := m["stream"]; ok {
		t.Error("stream was not stripped")
	}

	if _, ok := m["messages"]; !ok {
		t.Error("messages was dropped")
	}

	if string(m["anthropic_version"]) != `"bedrock-2023-05-31"` {
		t.Errorf("anthropic_version missing/wrong: %s", m["anthropic_version"])
	}
}

// TestNewAWSBedrockAuthRejectsBadMetadata covers validation before any STS
// call: malformed or incomplete metadata must error.
func TestNewAWSBedrockAuthRejectsBadMetadata(t *testing.T) {
	cases := []string{
		"",
		"not json",
		`{}`,
		`{"region":"ap-northeast-1"}`,
		`{"region":"ap-northeast-1","role_arn":"r","inference_profile_arn":"a"}`, // no aws_keys
		`{"region":"ap-northeast-1","role_arn":"r","inference_profile_arn":"a","aws_keys":[{}]}`, // empty keys
	}

	for _, in := range cases {
		if _, _, err := newAWSBedrockAuth(in); err == nil {
			t.Errorf("expected error for %q, got nil", in)
		}
	}
}
