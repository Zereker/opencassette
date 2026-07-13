# vendored/ — third-party recorded cassettes

Real request/response pairs that **other open-source projects** recorded
against live vendor APIs (using [pytest-recording](https://github.com/kiwicom/pytest-recording) /
VCR.py) and publish as an incidental by-product of their own test suites.
They are vendored here **as-is** — unmodified except for the auth-header
redaction the recording tools already applied — under the permissive licenses
(Apache-2.0 / MIT) that allow redistribution.

This is separate from the repo's `corpus/`, which holds opencassette's *own*
recordings against its scenario packs. `vendored/` is *borrowed* data: kept for
its value as a real-world reference of each vendor's wire shape (what a field
actually looks like on the wire, from a real SDK, not hand-written or
paraphrased from docs). Each source directory keeps that project's `LICENSE`
alongside the cassettes, as the licenses require on redistribution.

Load it in Go via `opencassette.Vendored()` (an `fs.FS` rooted here) and the
`cassette` package's loader, which handles both on-disk formats below plus
transparent gunzip (whole-file `*.yaml.gz` and gzipped bodies).

## Sources & licenses

| Directory | Upstream | License |
|---|---|---|
| `anthropic/simonw-llm-anthropic/` | [simonw/llm-anthropic](https://github.com/simonw/llm-anthropic) `tests/cassettes/test_anthropic/` | Apache-2.0 |
| `anthropic/langchain-ai-langchain/` | [langchain-ai/langchain](https://github.com/langchain-ai/langchain) `libs/partners/anthropic/tests/cassettes/` | MIT |
| `gemini/simonw-llm-gemini/` | [simonw/llm-gemini](https://github.com/simonw/llm-gemini) `tests/cassettes/test_gemini/` | Apache-2.0 |
| `cohere/langchain-ai-langchain-cohere/` | [langchain-ai/langchain-cohere](https://github.com/langchain-ai/langchain-cohere) `libs/cohere/tests/integration_tests/cassettes/` | MIT |
| `bedrock/langchain-ai-langchain-aws/` | [langchain-ai/langchain-aws](https://github.com/langchain-ai/langchain-aws) `libs/aws/tests/cassettes/` (Bedrock Converse API; stored as `*.yaml.gz`) | MIT |
| `openai/langchain-ai-langchain/` | [langchain-ai/langchain](https://github.com/langchain-ai/langchain) `libs/partners/openai/tests/cassettes/` | MIT |

## On-disk formats

- **pytest-recording / VCR.py**: a top-level `interactions:` list, each with a
  `request`/`response` pair (simonw's `llm-*` plugins, langchain-cohere).
- **langchain's own format**: parallel top-level `requests:` / `responses:`
  lists, index-aligned (the `langchain-ai/langchain` partner packages).

A body may be a plain string, a `!!binary` scalar (base64, possibly gzipped),
or nested under a `string:` key; the whole file may be gzip-compressed
(`*.yaml.gz`, used by langchain-aws to keep the repo small). The loader
normalizes all of that into plain bytes.

## Notes

- Auth headers (`x-api-key` / `authorization`) were already replaced with
  `**REDACTED**` by the recording tools; the files were re-checked for any
  leaked key, token, or signed URL and none were found.
- To add a new source: create a `<vendor>/<source-repo>/` directory, drop the
  original cassettes in as-is with the project's `LICENSE`, and add a row to
  the table above noting what it covers.
