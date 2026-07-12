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
| `cassette` (Go package) | Loads both on-disk cassette formats found in the wild (pytest-recording's `interactions:` and langchain's parallel lists), normalizing bodies (nested/`!!binary`/gzipped) into plain bytes |
| `recorder` (Go package) | An `http.RoundTripper` that records real calls with name-based **and** value-based credential scrubbing, plus a `meta:` provenance block |
| `scenario` (Go package) + `packs/` | Standard request-body packs (SDK-derived, coverage-enforced by tests) for four wire protocols — OpenAI chat, OpenAI Responses, Anthropic Messages, Gemini generateContent — so a recording session exercises tools, tool loops, streaming, structured output — not just `"hi"` |
| `verify` (Go package) | Checks a corpus for leaked credentials and synthetic-data tells (placeholder ids, impossible timestamps, token accounting that doesn't add up) |
| `cmd/opencassette` | The CLI over all of it: `record` and `verify` |
| `corpus/` | The recordings themselves, laid out `vendor/model/protocol/{stream,nostream}/scenario.yaml` |

## Quick start

Record a full scenario pack against a vendor (one cassette per scenario):

```sh
go build -o opencassette ./cmd/opencassette

RECORD_API_KEY=sk-... ./opencassette record \
  -url https://api.deepseek.com/chat/completions \
  -scenario-dir packs/openai-chat \
  -vendor deepseek -model deepseek-chat
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

Verify a corpus (CI runs this on every PR):

```sh
./opencassette verify corpus
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
