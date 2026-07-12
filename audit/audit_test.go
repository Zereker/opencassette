package audit

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/zereker/opencassette/scenario"
)

// miniSpec exercises the shapes real specs use: the request schema behind
// a $ref, whose target merges an allOf of another $ref plus inline
// properties — exactly how OpenAI's CreateChatCompletionRequest is built.
const miniSpec = `
paths:
  /chat/completions:
    post:
      requestBody:
        content:
          application/json:
            schema:
              $ref: '#/components/schemas/CreateReq'
components:
  schemas:
    Shared:
      properties:
        model: {type: string}
        temperature: {type: number}
    CreateReq:
      allOf:
        - $ref: '#/components/schemas/Shared'
        - properties:
            messages: {type: array}
            tools: {type: array}
`

const miniDiscovery = `{
  "schemas": {
    "GenerateContentRequest": {
      "properties": {"contents": {}, "tools": {}, "model": {}}
    }
  }
}`

func specServer(t *testing.T) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/openapi.yml", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(miniSpec))
	})
	mux.HandleFunc("/discovery.json", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(miniDiscovery))
	})
	srv := httptest.NewServer(mux)
	mux.HandleFunc("/stats.yml", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, "configured_endpoints: 1\nopenapi_spec_url: %s/openapi.yml\n", srv.URL)
	})
	t.Cleanup(srv.Close)

	return srv
}

func TestFieldsAllKinds(t *testing.T) {
	srv := specServer(t)
	want := []string{"messages", "model", "temperature", "tools"}

	for _, ref := range []*scenario.SpecRef{
		{Kind: "openapi", URL: srv.URL + "/openapi.yml", Path: "/chat/completions"},
		{Kind: "stainless-stats", URL: srv.URL + "/stats.yml", Path: "/chat/completions"},
	} {
		got, err := Fields(srv.Client(), ref)
		if err != nil {
			t.Fatalf("%s: %v", ref.Kind, err)
		}

		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: fields = %v, want %v ($ref/allOf resolution broken?)", ref.Kind, got, want)
		}
	}

	got, err := Fields(srv.Client(), &scenario.SpecRef{Kind: "discovery", URL: srv.URL + "/discovery.json", Schema: "GenerateContentRequest"})
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(got, []string{"contents", "model", "tools"}) {
		t.Errorf("discovery: fields = %v", got)
	}
}

func TestFieldsErrors(t *testing.T) {
	srv := specServer(t)
	for name, ref := range map[string]*scenario.SpecRef{
		"nil ref":         nil,
		"unknown kind":    {Kind: "grpc", URL: srv.URL},
		"missing path":    {Kind: "openapi", URL: srv.URL + "/openapi.yml", Path: "/nope"},
		"missing schema":  {Kind: "discovery", URL: srv.URL + "/discovery.json", Schema: "Nope"},
		"not a stats yml": {Kind: "stainless-stats", URL: srv.URL + "/discovery.json", Path: "/x"},
	} {
		if _, err := Fields(srv.Client(), ref); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestStainlessSpecURLResolves(t *testing.T) {
	srv := specServer(t)

	url, err := StainlessSpecURL(srv.Client(), srv.URL+"/stats.yml")
	if err != nil {
		t.Fatal(err)
	}

	if url != srv.URL+"/openapi.yml" {
		t.Errorf("resolved %q", url)
	}

	if _, err := StainlessSpecURL(srv.Client(), srv.URL+"/discovery.json"); err == nil {
		t.Error("non-stats file must not resolve")
	}
}

func TestCompare(t *testing.T) {
	r := Compare([]string{"a", "b", "x"}, []string{"a", "b", "c"})
	if r.SpecTotal != 3 || !reflect.DeepEqual(r.Covered, []string{"a", "b"}) ||
		!reflect.DeepEqual(r.Missing, []string{"c"}) || !reflect.DeepEqual(r.Extra, []string{"x"}) {
		t.Errorf("Compare: %+v", r)
	}
}

// TestPackFieldsRealPacks: the openai-chat union must include the synthetic
// probe fields (probing covers them even though no body carries them), and
// gemini's must not invent a model field.
func TestPackFieldsRealPacks(t *testing.T) {
	openai, err := scenario.LoadPack("../packs/openai-chat")
	if err != nil {
		t.Fatal(err)
	}

	fields := PackFields(openai)
	if !contains(fields, "stream_options") {
		t.Errorf("openai-chat pack fields must count synthetic probes: %v", fields)
	}

	gemini, err := scenario.LoadPack("../packs/gemini-generatecontent")
	if err != nil {
		t.Fatal(err)
	}

	if contains(PackFields(gemini), "model") {
		t.Error("gemini pack fields must not contain model")
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}

	return false
}
