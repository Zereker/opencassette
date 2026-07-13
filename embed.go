// Package opencassette exposes this repo's two recorded-traffic corpora as
// embedded filesystems, so downstream Go modules can read them with a plain
// `go get` — no git submodule and no checked-out working tree required:
//
//   - Corpus():   corpus/ — opencassette's own recordings, made against its
//     scenario packs. Layout:
//     <vendor>/<model>/<protocol>/<stream|nostream>/<scenario>.yaml, e.g.
//     zhipu/GLM-5.2/openai/nostream/chat_basic.yaml
//   - Vendored(): vendored/ — third-party cassettes (langchain partner
//     packages, simonw's llm-* plugins) recorded against live vendor APIs and
//     published under Apache-2.0 / MIT, vendored as-is with each source's
//     LICENSE and provenance (see vendored/README.md). Layout:
//     <vendor>/<source-repo>/<file>.yaml[.gz], e.g.
//     anthropic/simonw-llm-anthropic/test_tools.yaml
//
// Both are real, unmodified traffic; the two trees are kept separate so the
// self-recorded-vs-borrowed provenance stays unambiguous.
//
// The embed patterns are `all:<dir>`, so every file added under either tree —
// including whole new vendor/source directories — is picked up automatically.
// That also embeds READMEs and LICENSEs; consumers select cassettes by
// extension (*.yaml / *.yaml.gz), which is what the cassette package's
// loaders do.
package opencassette

import (
	"embed"
	"io/fs"
)

// CorpusFS holds the entire self-recorded corpus, keyed by its on-disk path
// (including the leading "corpus/"). Prefer Corpus() for prefix-free names.
//
//go:embed all:corpus
var CorpusFS embed.FS

// VendoredFS holds the entire third-party corpus, keyed by its on-disk path
// (including the leading "vendored/"). Prefer Vendored() for prefix-free names.
//
//go:embed all:vendored
var VendoredFS embed.FS

// Corpus returns the self-recorded corpus rooted at corpus/, so a cassette's
// name is exactly its corpus-relative path (no "corpus/" prefix).
func Corpus() fs.FS { return mustSub(CorpusFS, "corpus") }

// Vendored returns the third-party corpus rooted at vendored/, so a
// cassette's name is exactly its vendored-relative path (no "vendored/"
// prefix).
func Vendored() fs.FS { return mustSub(VendoredFS, "vendored") }

func mustSub(fsys embed.FS, dir string) fs.FS {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		// Unreachable: both dirs are compile-time-embedded.
		panic("opencassette: " + dir + " subtree missing from embed: " + err.Error())
	}

	return sub
}
