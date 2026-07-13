# Claude Code recording guide

This repository publishes real LLM API traffic. When asked to record a
vendor/model, follow this guide exactly. The corpus is useful only when the
traffic is real, provenanced, and safe to publish.

## Non-negotiable rules

- Never invent, hand-edit, or synthesize a cassette response.
- Never print, quote, summarize, log, or paste an API key.
- Never pass a key as a command-line flag. Supply it only through the
  `RECORD_API_KEY` environment variable of the recording process.
- Never copy a credential export into this repository or a repository-local
  temporary file.
- Disable shell tracing (`set +x`) before reading a key.
- Do not use `cat`, unfiltered `jq`, `grep`, `rg`, or Git diffs in a way that
  could print a key. Metadata-only inspection is allowed.
- Do not overwrite an existing cassette unless the user explicitly asks for
  a re-record and understands that reviewed evidence will be replaced.
- Do not run `git add`, `git commit`, `git push`, or open a pull request unless
  the user explicitly asks. Recording a model does not authorize submission.
- Do not modify application code merely to make a vendor accept a scenario.
  Record the observed behavior and report unsupported scenarios.

## Before recording

1. Read `README.md`, `FORMAT.md`, `CONTRIBUTING.md`, and the relevant section
   of `packs/README.md`.
2. Check `git status --short --branch`. Preserve unrelated and untracked user
   work.
3. Build the current CLI outside the repository output tree:

   ```sh
   go build -o /tmp/opencassette-record ./cmd/opencassette
   ```

4. Inspect a credential export only through non-secret fields such as entry
   index, `name`, `vendor`, `provider`, `endpoint`, `status`, and whether
   `api_key` is present. It is acceptable to report key length or equality;
   never report the value.
5. Select exactly one entry for each requested model. Confirm that its
   endpoint, vendor, model name, and key belong together. Do not silently use
   a similarly named proxy or another vendor's key.
6. List existing cassette paths and pack scenarios. Existing files must be
   skipped before spending an API call.

## Safe key extraction

Read the key and launch the recorder in one shell process. This example
requires an exact, unique entry name and never emits the selected value:

```sh
set +x
set -o pipefail
KEY_FILE=/absolute/path/to/export.json
MODEL_ENTRY='exact entry name'

RECORD_API_KEY="$(jq -er --arg name "$MODEL_ENTRY" \
  '[.[] | select(.name == $name) | .api_key] |
   if length == 1 then .[0] else error("expected exactly one key") end' \
  "$KEY_FILE")" \
  /tmp/opencassette-record record \
    --url 'https://vendor.example/v1/chat/completions' \
    --scenario-dir packs/openai-chat \
    --vendor vendor-name \
    --model exact-model-name \
    --timeout 5m \
    --pause 1s
```

Do not use `export RECORD_API_KEY=...`; keeping the assignment on the command
limits the credential to the recorder process. Do not put the resolved value
in a script, `.env` file, shell transcript, issue, or chat response.

The recorder prints a response preview. When tool output may be retained or
shared, filter it to operational status lines and preserve the recorder's exit
status:

```sh
set -o pipefail
# Append this to the safe one-process invocation above:
2>&1 | awk '/^===== scenario/ || /^HTTP / || /^wrote / || /^SKIPPED / ||
             /scenarios recorded/ || /^failed:/ || /^before publishing/ {print}'
```

Never write the unfiltered preview to a repository file.

## Recording behavior

- Prefer a full committed scenario pack over ad-hoc request bodies.
- The normal OpenAI-chat corpus layout is:

  ```text
  corpus/<vendor>/<model>/openai/<stream|nostream>/<scenario>.yaml
  ```

- A successful scenario is written only for HTTP 2xx.
- A batch may exit non-zero after writing its successful scenarios. This is
  expected when another scenario is rejected or an existing cassette is
  deliberately preserved.
- `chat_full_params` is intentionally broad. A vendor returning 400 does not
  invalidate the successful recordings. Report the rejection; do not create
  a fake cassette and do not weaken the committed scenario.
- Existing cassette errors are expected during a non-forced batch and should
  not trigger a re-record.
- Use `--force` only with explicit user authorization.
- For field-level compatibility evidence, run a separate
  `record --probe-fields <pack>` pass. Probe 400/422 responses belong under
  `fields-rejected/`; authentication failures, rate limits, and 5xx responses
  are errors, not evidence of field rejection.
- For protocols whose model or stream mode lives in the URL, follow the pack
  manifest and pass `{model}` and/or an explicit `--bucket` as documented.

## Automatic redaction

The recorder automatically:

- scrubs known credential headers;
- replaces the literal `RECORD_API_KEY` in headers, URIs, and bodies;
- discovers common trace/correlation headers such as `X-Log-Id`,
  `X-Request-Id`, `traceparent`, B3, AWS X-Ray, Google Cloud, and Datadog;
- replaces the same trace value consistently across headers, JSON, SSE, and
  URIs with markers such as `**TRACE_ID_1**`.

The rules are declarative and layered. The cross-vendor baseline is embedded
data (`internal/redact/baseline.yaml`), so it applies with no configuration.
A vendor may overlay extra carriers in `profiles/<vendor>.yaml`, loaded
automatically by `--vendor`:

- `credential_headers` — blanked to `**REDACTED**`;
- `trace_headers` — discovered and rewritten to `**TRACE_ID_n**`;
- `replacements` — custom substitutions on the recorded copy: a `header`
  name (whole value), a literal `find`, or a regexp `pattern`, each with a
  `with` value and an optional `in: [header, uri, body]` scope.

Prefer sinking a newly observed carrier into the vendor profile over a
one-off `--scrub-header`. Scrubbing and replacements touch only the recorded
copy — never the live request/response, nor the `meta` provenance block.

Automatic redaction is a safety net, not permission to skip verification or
human review. Vendor-specific secret and trace carriers may still exist.

## Mandatory checks after recording

1. Verify the entire corpus, not only the newly written file:

   ```sh
   /tmp/opencassette-record verify corpus
   ```

   There must be zero `FAIL` findings. Review every `WARN` before proceeding.

2. Check the exact source key against the recorded tree without printing a
   match. Reuse the same unique selector used for recording:

   ```sh
   set +x
   key="$(jq -er --arg name "$MODEL_ENTRY" \
     '[.[] | select(.name == $name) | .api_key] |
      if length == 1 then .[0] else error("expected exactly one key") end' \
     "$KEY_FILE")"

   if grep -R -F -q -- "$key" corpus; then
     echo 'FAIL: literal API key found in corpus' >&2
     unset key
     exit 1
   fi
   unset key
   echo 'literal key scan: clean'
   ```

   Never omit `-q`; printing the matching line would itself leak the key.

3. Confirm that known trace headers contain redaction markers and that the
   original trace value is absent from JSON and SSE bodies.
4. Read every new cassette as a human. Check prompts, model output, headers,
   error text, personal data, unexpected credentials, and provenance metadata.
5. Confirm `meta.vendor`, `meta.model`, `meta.endpoint`, `meta.scenario`,
   `meta.scenario_sha256`, and `meta.tool` are correct.
6. Run `git status --short` and list the new cassette paths. Ensure no key
   export, temporary log, binary, editor directory, or unrelated file appears.
7. If code was not changed, corpus verification is the required automated
   check. If code was changed under separate authorization, also run the
   repository's tests, vet, build, and lint checks.

## Handoff report

Report only safe operational facts:

- requested vendor and model;
- scenarios written and their HTTP status;
- scenarios rejected or skipped and why;
- total cassette count;
- corpus verifier result;
- `literal key scan: clean` or failure;
- whether trace markers were observed;
- current Git branch and whether files remain uncommitted.

Never include raw response bodies, raw trace IDs, API keys, credential-file
contents, or commands with resolved secrets in the handoff.
