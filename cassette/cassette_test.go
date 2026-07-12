package cassette

import (
	"strings"
	"testing"
)

func TestLoadInteractionsFormat(t *testing.T) {
	its, err := Load("testdata/interactions.yaml")
	if err != nil {
		t.Fatal(err)
	}

	if len(its) != 1 {
		t.Fatalf("want 1 interaction, got %d", len(its))
	}

	it := its[0]
	if it.Method != "POST" || it.URI != "https://api.example.com/v1/chat/completions" {
		t.Errorf("method/uri: %q %q", it.Method, it.URI)
	}

	if !strings.Contains(string(it.RequestBody), `"model":"example-model"`) {
		t.Errorf("request body: %q", it.RequestBody)
	}

	if !strings.Contains(string(it.ResponseBody), `"id":"chatcmpl-abc123"`) {
		t.Errorf("response body: %q", it.ResponseBody)
	}
}

// TestLoadIgnoresMeta: the recorder's provenance block must not break or
// leak into loading.
func TestLoadIgnoresMeta(t *testing.T) {
	its, err := Load("testdata/interactions.yaml")
	if err != nil {
		t.Fatal(err)
	}

	if len(its) != 1 {
		t.Fatalf("meta block changed the interaction count: %d", len(its))
	}
}

func TestLoadParallelFormatWithBinaryBody(t *testing.T) {
	its, err := Load("testdata/parallel.yaml")
	if err != nil {
		t.Fatal(err)
	}

	if len(its) != 1 {
		t.Fatalf("want 1 interaction, got %d", len(its))
	}

	if !strings.HasPrefix(string(its[0].ResponseBody), "data: ") {
		t.Errorf("!!binary SSE body not decoded: %q", its[0].ResponseBody)
	}

	if !strings.Contains(string(its[0].ResponseBody), "data: [DONE]") {
		t.Errorf("SSE terminator lost: %q", its[0].ResponseBody)
	}
}

func TestLoadWholeFileGzip(t *testing.T) {
	plain, err := Load("testdata/interactions.yaml")
	if err != nil {
		t.Fatal(err)
	}

	gz, err := Load("testdata/whole-file.yaml.gz")
	if err != nil {
		t.Fatal(err)
	}

	if len(gz) != len(plain) || string(gz[0].ResponseBody) != string(plain[0].ResponseBody) {
		t.Errorf("gzipped file did not load identically to its plain twin")
	}
}

func TestLoadDir(t *testing.T) {
	files, err := LoadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}

	if len(files) != 3 {
		t.Fatalf("want 3 files, got %d: %v", len(files), SortedKeys(files))
	}

	keys := SortedKeys(files)
	for i := 1; i < len(keys); i++ {
		if keys[i-1] >= keys[i] {
			t.Errorf("SortedKeys not sorted: %v", keys)
		}
	}
}
