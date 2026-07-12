# Cassette format

opencassette writes (and the `cassette` package reads) pytest-recording /
VCR.py's `interactions:` format, extended with one top-level `meta:` block.
Files written by this tool remain loadable by Python VCR tooling, which
ignores unknown top-level keys — as does this repo's loader, so
provenance-carrying and provenance-less files load identically.

```yaml
meta:
  recorded_at: "2026-07-12T08:00:00Z"   # RFC3339 UTC, stamped at write time
  vendor: deepseek                       # corpus vendor segment
  model: deepseek-chat                   # model as sent upstream
  endpoint: https://api.deepseek.com     # scheme://host actually called
  scenario: chat_basic                   # scenario-pack name, if batch-recorded
  scenario_sha256: 9f3c…                 # SHA-256 of the pack scenario file as committed
                                         # (pre model-substitution), if batch-recorded
  tool: opencassette/0.1.0
interactions:
- request:
    body: '{"model":"deepseek-chat","messages":[...]}'
    headers:
      Authorization:
      - '**REDACTED**'
      Content-Type:
      - application/json
    method: POST
    uri: https://api.deepseek.com/chat/completions
  response:
    body:
      string: '{"id":"...","choices":[...],"usage":{...}}'
    headers:
      Content-Type:
      - application/json
    status:
      code: 200
      message: OK
```

## Bodies

- UTF-8 text (JSON, SSE) is stored as a plain YAML string.
- Non-UTF-8 bytes (e.g. AWS event-stream framing) are stored as a `!!binary`
  scalar and round-trip byte-for-byte.
- The loader also transparently gunzips: a body whose bytes start with the
  gzip magic number, or a whole file named `*.yaml.gz`.

## Scrubbing (mandatory)

- Credential-bearing headers (`authorization`, `x-api-key`, `api-key`,
  `x-goog-api-key`, `x-auth-token`, `x-amz-security-token`, `cookie`,
  `set-cookie`, `proxy-authorization`) are replaced with `**REDACTED**`.
- The literal API key value is additionally replaced wherever its bytes
  appear — any header, the URI query string, including URL-escaped
  spellings — so nonstandard auth carriers can't leak it.
- `verify` treats any surviving credential-shaped string as a hard failure.

## Corpus layout

```
corpus/<vendor>/<model>/<protocol>/<stream|nostream>/<scenario>.yaml
```

- `protocol` is the wire protocol recorded (`openai`, `anthropic`, …).
- The stream bucket comes from the request body's own `"stream"` field.
- A multi-turn scenario (recorded with `-append`) stays in whichever bucket
  its first turn landed in — the bucket classifies the scenario, and a file
  can't live in two directories.

## Second format the loader accepts (read-only)

For importing existing third-party captures, the loader also parses
langchain's variant — parallel top-level `requests:` / `responses:` lists,
index-aligned. The recorder never writes this format.
