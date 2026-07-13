# opencassette

Real recorded LLM API traffic, as an open corpus and a toolchain: **record**,
**scrub**, **verify**, and **load** vendor cassettes.

[中文说明 / Chinese README](README.zh-CN.md)

## Why

Everything that translates between LLM APIs — gateways, SDKs, proxies,
agent frameworks — needs real request/response data to test against, because
vendor docs and hand-written fixtures drift from what the wire actually
carries. Today that data barely exists as a shared resource:

- A few projects (langchain partner packages, simonw's `llm-*` plugins)
  publish VCR cassettes as an incidental by-product of their test suites —
  scattered, format-inconsistent, and covering only the vendors those
  projects happen to integrate.
- litellm records real traffic but into a Redis cache with a 24-hour TTL —
  deliberately ephemeral, nothing committed.
- For entire vendor ecosystems (DeepSeek, Zhipu GLM, MiniMax, …) **no public
  recorded traffic exists at all** — and the demand is real enough that
  public repos have been found committing *hand-written fakes* styled as
  recordings, with placeholder ids like `chatcmpl-verify-001` and epoch
  timestamps, which is worse than no data.

opencassette is the missing piece: a standard on-disk format with provenance,
a recorder with credential scrubbing built in, scenario packs that make each
recording cover a real SDK's parameter surface, an authenticity verifier
tuned to the fakes actually observed in the wild, and a corpus layout to
grow a shared library of real captures.

## What's inside

| Piece | What it does |
|---|---|
| `cassette` (public Go package) | Loads both on-disk cassette formats found in the wild (pytest-recording's `interactions:` and langchain's parallel lists), normalizing bodies (nested/`!!binary`/gzipped) into plain bytes — the package you import to replay captures |
| `internal/recorder` | An `http.RoundTripper` that records real calls, delegating all scrubbing to `redact` and adding a `meta:` provenance block |
| `internal/redact` + `profiles/` | The redaction policy: an embedded cross-vendor baseline (credential headers, plus trace/correlation carriers rewritten to `**TRACE_ID_n**`), per-vendor overlays in `profiles/<vendor>.yaml`, and custom header/find/pattern replacements — applied to the recorded copy only |
| `internal/scenario` + `packs/` | Standard request-body packs (SDK-derived, coverage-enforced by tests) for four wire protocols — OpenAI chat, OpenAI Responses, Anthropic Messages, Gemini generateContent — so a recording session exercises tools, tool loops, streaming, structured output — not just `"hi"` |
| `internal/verify` | Checks a corpus for leaked credentials and synthetic-data tells (placeholder ids, impossible timestamps, token accounting that doesn't add up) |
| `internal/audit` | Diffs each pack's field coverage against the protocol's authoritative schema (OpenAI/Anthropic via their SDKs' published OpenAPI specs, Gemini via Google's discovery document) — a one-way ceiling check that suggests what to record next, never a validator of recorded traffic |
| `cmd/opencassette` | The CLI over all of it: `record`, `verify` and `audit` |
| `corpus/` | The recordings themselves, laid out `vendor/model/protocol/{stream,nostream}/scenario.yaml` |

## Quick start

Record a full scenario pack against a vendor (one cassette per scenario):

```sh
go build -o opencassette ./cmd/opencassette

RECORD_API_KEY=sk-... ./opencassette record \
  --url https://api.deepseek.com/chat/completions \
  --scenario-dir packs/openai-chat \
  --vendor deepseek --model deepseek-chat
# -> corpus/deepseek/deepseek-chat/openai/{stream,nostream}/<scenario>.yaml
```

Other protocols work the same way — the pack's `pack.json` manifest tells
the recorder where the model and stream flag live (Gemini carries both in
the URL):

```sh
RECORD_API_KEY=... ./opencassette record \
  -url 'https://generativelanguage.googleapis.com/v1beta/models/{model}:generateContent' \
  -scenario-dir packs/gemini-generatecontent \
  -vendor google -model gemini-2.0-flash -auth query:key -bucket nostream

RECORD_API_KEY=sk-ant-... ./opencassette record \
  -url https://api.anthropic.com/v1/messages \
  -scenario-dir packs/anthropic-messages \
  -vendor anthropic -model claude-sonnet-4-5 \
  -auth x-api-key -header 'anthropic-version: 2023-06-01'
```

Probe which request fields a vendor actually supports — one minimal call
per field, accepted fields recorded under `fields/`, 400/422 rejections
(evidence of non-support) under `fields-rejected/`, plus a
`field-support.json` matrix:

```sh
RECORD_API_KEY=sk-... ./opencassette record \
  -url https://api.deepseek.com/chat/completions \
  -probe-fields packs/openai-chat \
  -vendor deepseek -model deepseek-chat
```

### Redaction and vendor profiles

Every recording is scrubbed against a built-in cross-vendor baseline —
credential headers blanked, and trace/correlation carriers (`traceparent`,
B3, `X-Request-Id`, …) rewritten to stable `**TRACE_ID_n**` markers,
consistently across headers, URI and bodies. The baseline
(`internal/redact/baseline.yaml`) is embedded in the binary, so a capture is
safe even with no profile.

A vendor can declare extra carriers in `profiles/<vendor>.yaml`, loaded
automatically by `--vendor`:

```yaml
# profiles/azure.yaml
trace_headers:
  - apim-request-id
  - azureml-model-session
replacements:                    # custom substitutions on the recorded copy
  - header: x-ms-region          # replace a whole header value by name
    with: "**REGION**"
  - find: "my-resource-name"     # literal substring
    with: "**RESOURCE**"
    in: [uri, body]
  - pattern: 'org-[a-z0-9]{24}'  # regexp match, body only
    with: "**ORG_ID**"
    in: [body]
```

Replacements run after the baseline scrubbing and touch only the recorded
copy — never the live request/response, nor the `meta` provenance block.
Prefer sinking a newly observed carrier into the vendor profile over a one-off
`--scrub-header`. Automatic redaction is a safety net, not a substitute for
reading the file and running `verify` before publishing.

Verify a corpus (CI runs this on every PR):

```sh
./opencassette verify corpus
```

Audit pack coverage against each protocol's authoritative spec (network;
advisory — the report is a to-record list, not a gate):

```sh
./opencassette audit packs
# == packs/anthropic-messages (anthropic) vs .../anthropic-sdk-python/main/.stats.yml
#    covered 9/18 spec fields
#    missing from pack (9): cache_control, ..., system, tool_choice, top_k, top_p
```

Load cassettes in your own tests:

```go
import "github.com/zereker/opencassette/cassette"

its, err := cassette.Load("corpus/deepseek/deepseek-chat/openai/nostream/chat_basic.yaml")
// its[0].RequestBody / its[0].ResponseBody are plain bytes, ready to replay
```

## Format

See [FORMAT.md](FORMAT.md) — pytest-recording's `interactions:` list plus a
`meta:` provenance block, credentials scrubbed to `**REDACTED**`. Files
written by this tool load unmodified in Python's VCR.py ecosystem too.

## Contributing recordings

See [CONTRIBUTING.md](CONTRIBUTING.md). The short version: recordings must
be real (the verifier and the review process both exist to enforce that),
scrubbed, and carry provenance. Synthetic data is not accepted — it defeats
the project's entire reason to exist.

## License

Apache-2.0. Cassettes contain model-generated output; recordings are
contributed under this repo's license, and no recording may contain
credentials or personal data.
