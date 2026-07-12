# packs/ — standard request-body scenario packs

Input to `opencassette record -scenario-dir`: instead of hand-writing a
minimal request on recording day, every scenario in a pack is sent to the
vendor's real API in one batch — so a recorded cassette set covers the same
request-parameter surface a real SDK exercises. That request-side realism is
exactly what made existing third-party cassettes (langchain's, simonw's)
valuable, and exactly what ad-hoc recording silently loses.

At record time only the `"model"` field is substituted (`-model` flag);
every other byte is sent as the pack defines it.

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

## Adding scenarios

Provenance order of preference: verbatim real SDK request > derived from
real data by a documented transformation > hand-written (with justification
for why no real source exists). Document every addition in the table above.
