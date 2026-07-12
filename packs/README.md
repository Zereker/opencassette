# packs/ — standard request-body scenario packs

Input to `opencassette record -scenario-dir`: instead of hand-writing a
minimal request on recording day, every scenario in a pack is sent to the
vendor's real API in one batch — so a recorded cassette set covers the same
request-parameter surface a real SDK exercises. That request-side realism is
exactly what made existing third-party cassettes (langchain's, simonw's)
valuable, and exactly what ad-hoc recording silently loses.

At record time only the model is substituted (`-model` flag); every other
byte is sent as the pack defines it.

## pack.json — the per-protocol manifest

Wire protocols differ in more than field names, so each pack declares how
its bodies work:

```json
{
  "protocol": "gemini",     // corpus path segment recordings land under
  "required": ["contents"], // top-level fields every scenario must carry
  "model_field": "",        // body field for the model; "" = model rides in the URL
  "stream_field": ""        // body field signaling streaming; "" = the endpoint decides
}
```

A pack without a manifest gets the OpenAI-chat defaults (`model` /
`messages` required, `model` substituted, `stream` bucketed). For
URL-model packs put a `{model}` placeholder in `-url`; for URL-streamed
packs (Gemini's `:streamGenerateContent`) record twice with an explicit
`-bucket stream` / `-bucket nostream`.

A manifest may also carry a `spec` block naming the protocol's
authoritative request schema — OpenAI and Anthropic publish theirs via
their SDK repos' `.stats.yml` (`"kind": "stainless-stats"`), Gemini via
Google's discovery document (`"kind": "discovery"`), and a plain
`"kind": "openapi"` URL works too. `opencassette audit packs` fetches
each spec and diffs it against the pack's field union: `missing` fields
are the to-record list (each new scenario still needs real provenance —
the spec names the gap, it doesn't provide the body), `extra` fields are
vendor extensions or spec drift worth investigating. The audit is
one-way by design: the spec is a growth ceiling for packs, never a
validator of recorded traffic — vendors' deviations from their own specs
are data, not errors.

`stainless-stats` follows the SDK repo's pointer, so audits always see
the *current* ceiling — new upstream fields show up the day they ship.
When you want determinism instead (the same audit result on every run),
pin the spec: `opencassette audit -resolve packs` prints each pack's
resolved spec URL — Stainless URLs are content-addressed, so pasting one
into pack.json as `{"kind": "openapi", "url": "...", "path": "..."}`
freezes the ceiling, and raising it becomes an explicit one-line diff.

## `openai-chat/` — scenarios and provenance

No body in this pack was invented. Sources fall in three classes: verbatim
real SDK requests, minimal derivations of real SDK requests, and a curated
full-parameter matrix:

| Scenario | Parameter surface | Provenance |
|---|---|---|
| `chat_basic.json` | minimal non-streaming chat | derived: the real `ChatOpenAI` request recorded in [langchain-ai/langchain](https://github.com/langchain-ai/langchain) `libs/partners/openai/tests/cassettes/TestOpenAIStandard.test_stream_time.yaml` (interaction 0), with only `stream`/`stream_options` removed |
| `chat_stream_usage.json` | `stream` + `stream_options.include_usage` | verbatim: same cassette, interaction 0, unmodified |
| `tools_named_choice_stream.json` | `tools` + **named** `tool_choice` + `temperature:0` + streaming | verbatim: same repo, `test_streaming_tool_call_v1_v2_parity.yaml` (interaction 0) |
| `structured_output_json_schema.json` | `response_format: json_schema` (`strict:true`, `additionalProperties:false`) + explicit `stream:false` | verbatim: same repo, `test_schema_parsing_failures.yaml` (interaction 2) |
| `tool_loop_round_trip.json` | multi-turn tool loop: assistant with **two parallel** `tool_calls` + two `role:"tool"` results — the shape protocol translators most often get wrong | derived: the real Anthropic SDK request in [simonw/llm-anthropic](https://github.com/simonw/llm-anthropic) `tests/cassettes/test_anthropic/test_tools.yaml` (interaction 1), converted to OpenAI shape by [llm-gateway](https://github.com/zereker/llm-gateway)'s `anthropic_openai` reverse translator — the data's origin is still a real SDK |
| `chat_full_params.json` | the full-parameter matrix (23 top-level fields: `n`/`seed`/`stop`/`presence_penalty`/`modalities`/`prediction`/`web_search_options`/…) | curated: llm-gateway's field-matrix fixture of every field a real upstream is known to accept. Strict vendors may 4xx on unknown fields — batch mode skips and reports, leaving the judgment to the operator |

**Coverage is test-enforced**: the `scenario` package's tests require the
pack's union to carry every top-level field `chat_full_params.json`
declares, plus the structural shapes (both stream buckets, named
tool_choice, a tool-result round trip with parallel calls, json_schema
output). Removing a field's only carrier turns CI red.

## Field probing (`record -probe-fields packs/openai-chat`)

The full-params scenario can only prove "this exact bundle was accepted";
a strict vendor's 4xx on it says nothing about which fields it *does*
support, and a lenient vendor silently ignoring a field looks identical to
supporting it. Probe mode turns the same two committed files into
per-field evidence: for every top-level field of `chat_full_params.json`
(except `model`/`messages`) it sends `chat_basic.json`'s minimal body plus
that one field, in isolation.

Four fields are adjusted so the probe measures capability rather than
request-validation noise (the rules live in `scenario.BuildProbes` and are
test-pinned):

| Field | Adjustment | Why |
|---|---|---|
| `tool_choice`, `parallel_tool_calls` | carries `tools` from the full-params body | only valid alongside declared tools |
| `stream` | sent as `true` | the committed `false` is treated as absent by every vendor |
| `audio` | carries `modalities: ["text","audio"]` | audio output is rejected unless the modality is requested |
| `stream_options` | synthetic probe — `{"include_usage": true}` riding on `stream: true` (value mirrors `chat_stream_usage.json`) | can't live in the full-params body (rejected when `stream` is false), but it's the only way a recorded stream carries usage/token accounting |

Results: 2xx cassettes land in `fields/`, 400/422 rejections (evidence of
non-support, error body included) in `fields-rejected/`, and a
`field-support.json` matrix is written alongside — see FORMAT.md.

## `anthropic-messages/` — scenarios and provenance

Claude Messages API (`/v1/messages`, `-auth x-api-key`, remember
`-header 'anthropic-version: 2023-06-01'`). Sources: real Anthropic SDK
requests recorded in [simonw/llm-anthropic](https://github.com/simonw/llm-anthropic)
`tests/cassettes/test_anthropic/` and
[langchain-ai/langchain](https://github.com/langchain-ai/langchain)
`libs/partners/anthropic/tests/cassettes/`:

| Scenario | Parameter surface | Provenance |
|---|---|---|
| `msg_stream.json` | minimal streaming chat (`max_tokens`/`temperature`) | verbatim: llm-anthropic `test_prompt.yaml` (interaction 0) |
| `msg_basic.json` | minimal non-streaming chat | derived: same body with only `stream` removed |
| `msg_multi_turn.json` | assistant-turn replay across turns | verbatim: llm-anthropic `test_async_prompt.yaml` (interaction 1) |
| `msg_prefill_stop_sequences.json` | assistant **prefill** (conversation ends on an assistant turn) + `stop_sequences` — both Anthropic-unique shapes | verbatim: llm-anthropic `test_prompt_with_prefill_and_stop_sequences.yaml` |
| `msg_thinking.json` | `thinking.budget_tokens` | verbatim: llm-anthropic `test_thinking_prompt.yaml` |
| `msg_structured_output.json` | `output_config.format` json_schema | verbatim: llm-anthropic `test_schema_prompt.yaml` |
| `msg_tools_strict.json` | tool definition with `strict: true`, non-streaming | verbatim: langchain-anthropic `test_strict_tool_use.yaml.gz` (request 0) |
| `msg_tool_loop_round_trip.json` | **two parallel** `tool_use` blocks + `tool_result` blocks — the round trip translators most often get wrong | verbatim: llm-anthropic `test_tools.yaml` (interaction 1) |

Not yet carried (no public real capture found): `system`, `tool_choice`,
`top_k`, `metadata`. Contributions welcome — with provenance.

## `gemini-generatecontent/` — scenarios and provenance

Gemini `generateContent` (model in the **URL**, not the body — record with
`-url '.../v1beta/models/{model}:generateContent'`; streaming is the
`:streamGenerateContent?alt=sse` endpoint — run the pack once per bucket
with `-bucket`; auth is `-auth query:key` for AI Studio or
`-auth x-goog-api-key`-style headers). Sources: real
google-generativeai-SDK-shaped requests recorded in
[simonw/llm-gemini](https://github.com/simonw/llm-gemini)
`tests/cassettes/test_gemini/`:

| Scenario | Parameter surface | Provenance |
|---|---|---|
| `content_basic.json` | `contents` + `safetySettings` | verbatim: `test_prompt.yaml` (interaction 0) |
| `content_tools.json` | `tools.functionDeclarations` | verbatim: `test_tools.yaml` (interaction 0) |
| `content_tool_loop_round_trip.json` | `function_call` part + `function_response` part round trip | verbatim: `test_tools.yaml` (interaction 1) |
| `content_structured_output.json` | `generationConfig.response_mime_type` + `response_schema` | verbatim: `test_prompt_with_pydantic_schema.yaml` |
| `content_structured_array.json` | nested **array** response schema (`items`) — the nesting translators botch | verbatim: `test_prompt_with_multiple_dogs.yaml` |

Not yet carried: `systemInstruction`, `generationConfig` sampling params
(`temperature`/`topK`/`topP`), `cachedContent`.

## `openai-responses/` — scenarios and provenance

OpenAI Responses API (`/v1/responses`, bearer auth). Sources: real
openai-SDK requests recorded in
[langchain-ai/langchain](https://github.com/langchain-ai/langchain)
`libs/partners/openai/tests/cassettes/` (the `test_codex_*` suite runs
exclusively against the Responses API):

| Scenario | Parameter surface | Provenance |
|---|---|---|
| `resp_stream.json` | minimal streaming request (`input` items) | verbatim: `TestOpenAIResponses.test_stream_time.yaml.gz` (request 0) |
| `resp_basic.json` | minimal non-streaming request | derived: same body with only `stream` removed |
| `resp_instructions.json` | `instructions` + `store: false` | verbatim: `test_codex_invoke.yaml.gz` |
| `resp_reasoning.json` | `reasoning.effort` | verbatim: `test_codex_reasoning.yaml.gz` |
| `resp_tools.json` | function `tools` (flat Responses-API shape, not chat's nested one) | verbatim: `test_codex_function_calling.yaml.gz` (request 0) |
| `resp_tool_loop_round_trip.json` | `function_call` + `function_call_output` input items round trip | verbatim: `test_codex_agent_loop.yaml.gz` (request 1) |
| `resp_structured_output.json` | `text.format` json_schema (`strict: true`) | verbatim: `test_codex_structured_output_pydantic.yaml.gz` |

Not yet carried: `previous_response_id`, built-in tools
(`web_search`/`file_search`), `include`, `truncation`.

## Adding scenarios

Provenance order of preference: verbatim real SDK request > derived from
real data by a documented transformation > hand-written (with justification
for why no real source exists). Document every addition in the table above.
