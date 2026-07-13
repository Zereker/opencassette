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
  appear — headers, bodies and the URI query string, including URL-escaped
  spellings — so nonstandard auth carriers can't leak it.
- Trace/correlation headers (`traceparent`, `x-request-id`, `x-log-id`,
  `x-trace-id`, B3, AWS X-Ray, Google Cloud and Uber carriers) are
  automatically scrubbed. A discovered identifier is replaced consistently
  in headers, URIs, JSON bodies and SSE chunks with a marker such as
  `**TRACE_ID_1**`, preserving correlation without publishing the original.
- A vendor whose auth rides in a header outside the default list must be
  recorded with `-scrub-header <name>` (repeatable); `verify` flags any
  secret-shaped header value as a hard failure, as a second net.
- `verify` treats any surviving credential-shaped string as a hard failure.
- `verify` treats a raw value in a known trace/correlation header as a hard
  failure.

## Corpus layout

```
corpus/<vendor>/<model>/<protocol>/<stream|nostream>/<scenario>.yaml
```

- `protocol` is the wire protocol recorded (`openai`, `anthropic`,
  `gemini`, `openai-responses`, …), defaulting to the pack manifest's.
- The stream bucket comes from the request body's stream field (per the
  pack manifest); protocols that signal streaming in the URL instead
  (Gemini's `:streamGenerateContent`) are recorded with an explicit
  `-bucket`.
- A multi-turn scenario (recorded with `-append`) stays in whichever bucket
  its first turn landed in — the bucket classifies the scenario, and a file
  can't live in two directories.

A field-probe run (`record -probe-fields`) adds three siblings under the
same `<protocol>/` directory:

```
corpus/<vendor>/<model>/<protocol>/fields/<field>.yaml           # accepted: minimal request + one field, HTTP 2xx
corpus/<vendor>/<model>/<protocol>/fields-rejected/<field>.yaml  # the vendor's 400/422 — recorded evidence of non-support
corpus/<vendor>/<model>/<protocol>/field-support.json            # machine-readable support matrix for the run
```

A probe cassette's `meta.scenario` is `field:<name>`, and its
`meta.scenario_sha256` hashes the pack's `chat_full_params.json` the field
value came from. Statuses other than 2xx/400/422 (auth failures, rate
limits, 5xx) say nothing about the field: no cassette is written and the
matrix marks the field `error`.

## Second format the loader accepts (read-only)

For importing existing third-party captures, the loader also parses
langchain's variant — parallel top-level `requests:` / `responses:` lists,
index-aligned. The recorder never writes this format.
