package opencassette

import (
	"embed"
	"io/fs"
)

// VendoredFS embeds the third-party cassette corpus under vendored/ — real
// request/response pairs that other open-source projects (langchain partner
// packages, simonw's llm-* plugins) recorded against live vendor APIs and
// publish under permissive licenses (Apache-2.0 / MIT), vendored here as-is
// with their LICENSE and provenance (see vendored/README.md).
//
// This is distinct from CorpusFS: corpus/ is opencassette's *own* recordings
// against its scenario packs; vendored/ is *borrowed* third-party fixtures.
// Both are real, unmodified traffic; keeping them in separate trees keeps the
// self-recorded-vs-vendored provenance unambiguous.
//
// Paths carry the "vendored/" prefix, matching the on-disk layout, e.g.:
//
//	vendored/anthropic/simonw-llm-anthropic/test_tools.yaml
//	vendored/bedrock/langchain-ai-langchain-aws/test_agent_loop[v0].yaml.gz
//
// The pattern is `all:vendored`, so any file added under vendored/ — including
// a whole new source directory — is picked up automatically. That also embeds
// the LICENSE and README files; consumers select cassettes by extension
// (*.yaml / *.yaml.gz), which is what the cassette package's loader does.
//
//go:embed all:vendored
var VendoredFS embed.FS

// Vendored returns the third-party corpus rooted at the vendored/ directory,
// so a cassette's name is exactly its vendored-relative path (no "vendored/"
// prefix) — e.g. "cohere/langchain-ai-langchain-cohere/test_invoke_tool_calls.yaml".
func Vendored() fs.FS {
	sub, err := fs.Sub(VendoredFS, "vendored")
	if err != nil {
		// Unreachable: "vendored" is a compile-time-embedded directory.
		panic("opencassette: vendored subtree missing from embed: " + err.Error())
	}

	return sub
}
