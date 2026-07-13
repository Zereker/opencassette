package opencassette_test

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/zereker/opencassette"
	"github.com/zereker/opencassette/cassette"
)

// TestVendoredEmbed checks the third-party corpus is embedded intact: a
// healthy number of cassettes, every source directory's LICENSE carried along,
// and two known files present with the right on-disk shape (a plain-yaml
// pytest-recording cassette, and a gzip-compressed .yaml.gz cassette).
func TestVendoredEmbed(t *testing.T) {
	vfs := opencassette.Vendored()

	var cassettes, licenses int

	err := fs.WalkDir(vfs, ".", func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		switch {
		case strings.HasSuffix(p, ".yaml"), strings.HasSuffix(p, ".yaml.gz"):
			cassettes++
		case strings.HasSuffix(p, "LICENSE"):
			licenses++
		}

		return nil
	})
	if err != nil {
		t.Fatalf("walk Vendored(): %v", err)
	}

	if cassettes < 100 {
		t.Errorf("want >=100 vendored cassettes, got %d", cassettes)
	}

	if licenses < 6 {
		t.Errorf("want a LICENSE per source (>=6), got %d", licenses)
	}

	// Plain-yaml cassette: a pytest-recording `interactions:` document.
	plain, err := fs.ReadFile(vfs, "anthropic/simonw-llm-anthropic/test_tools.yaml")
	if err != nil {
		t.Fatalf("read plain yaml: %v", err)
	}

	if !strings.Contains(string(plain), "interactions:") {
		t.Errorf("plain yaml missing interactions: header")
	}

	// Gzipped whole-file cassette (langchain-aws Converse): must round-trip as
	// binary, i.e. still start with the gzip magic bytes.
	gz, err := fs.ReadFile(vfs, "bedrock/langchain-ai-langchain-aws/test_agent_loop[v0].yaml.gz")
	if err != nil {
		t.Fatalf("read gzipped yaml: %v", err)
	}

	if len(gz) < 2 || gz[0] != 0x1f || gz[1] != 0x8b {
		t.Errorf("gzipped cassette lost its gzip magic (text-converted on checkout?)")
	}
}

// TestLoadDirFSOverEmbeddedCorpora loads both embedded corpora through the
// cassette package's fs.FS loader end-to-end — the exact path downstream
// consumers use — asserting every file parses and nothing loads empty.
func TestLoadDirFSOverEmbeddedCorpora(t *testing.T) {
	for name, fsys := range map[string]fs.FS{
		"corpus":   opencassette.Corpus(),
		"vendored": opencassette.Vendored(),
	} {
		all, err := cassette.LoadDirFS(fsys)
		if err != nil {
			t.Fatalf("%s: LoadDirFS: %v", name, err)
		}

		if len(all) < 50 {
			t.Errorf("%s: suspiciously few cassettes: %d", name, len(all))
		}

		for _, p := range cassette.SortedKeys(all) {
			if len(all[p]) == 0 {
				t.Errorf("%s/%s: loaded 0 interactions", name, p)
			}
		}
	}
}
