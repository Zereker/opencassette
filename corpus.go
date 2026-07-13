// Package opencassette exposes the recorded-traffic corpus as an embedded
// filesystem, so downstream Go modules can read it with a plain `go get` —
// no git submodule and no checked-out working tree required.
//
// CorpusFS embeds the whole corpus/ directory tree. Paths are prefixed with
// "corpus/", matching the on-disk layout, e.g.:
//
//	corpus/zhipu/GLM-5.2/openai/nostream/chat_basic.yaml
//	corpus/aws/claude-opus-4-6/anthropic/nostream/msg_basic.yaml
//
// Use Corpus() for a filesystem rooted at corpus/ (paths without the prefix),
// or walk CorpusFS directly. The embed pattern is `all:corpus`, so every file
// added under corpus/ — including whole new vendor directories — is picked up
// automatically without editing this file. That also embeds the corpus README;
// consumers should select cassettes by extension (*.yaml / *.yaml.gz), which
// is what the cassette package's loader does.
package opencassette

import (
	"embed"
	"io/fs"
)

// CorpusFS holds the entire recorded corpus, keyed by its on-disk path
// (including the leading "corpus/").
//
//go:embed all:corpus
var CorpusFS embed.FS

// Corpus returns the corpus filesystem rooted at the corpus/ directory, so a
// cassette's name is exactly its corpus-relative path (no "corpus/" prefix) —
// e.g. "zhipu/GLM-5.2/openai/nostream/chat_basic.yaml".
func Corpus() fs.FS {
	sub, err := fs.Sub(CorpusFS, "corpus")
	if err != nil {
		// Unreachable: "corpus" is a compile-time-embedded directory.
		panic("opencassette: corpus subtree missing from embed: " + err.Error())
	}

	return sub
}
