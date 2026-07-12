# Contributing

## Recordings (the corpus)

The corpus is only worth anything if every file in it is a real capture.
Public repos have been found committing hand-written "cassettes" styled as
recordings — placeholder ids, epoch timestamps, token counts that don't add
up. **Synthetic data is not accepted here under any framing** ("it's just a
placeholder", "the shape is right"): a consumer who tests against a fake
learns nothing, and the corpus loses the one property that distinguishes it
from every project's local fixtures.

A recording PR must:

1. **Be recorded with this repo's tool** (`opencassette record`), or be an
   import of a third-party project's published cassettes with license and
   provenance documented. Self-recorded files carry the `meta:` block the
   tool writes; don't strip it.
2. **Pass `opencassette verify`** — CI runs it on every PR. FAIL findings
   (credential leaks, impossible timestamps, unscrubbed headers) block the
   merge. WARN findings need a reviewer's explicit judgment in the PR
   thread.
3. **Be read by a human before submitting.** Scrubbing removes the
   credentials the tool knows about — not secrets a response body might echo
   back, and not personal data in your prompts. Don't record prompts you
   wouldn't publish.
4. **Land in the standard layout**:
   `corpus/<vendor>/<model>/<protocol>/<stream|nostream>/<scenario>.yaml`.
   Prefer recording a whole scenario pack (`-scenario-dir packs/openai-chat`)
   over a single ad-hoc body — the packs exist so every vendor's coverage is
   comparable.

Reviewers additionally check plausibility: real response ids, model/version
strings consistent with the recorded date, latency-shaped SSE chunking,
usage numbers that add up. If provenance can't be established, the PR
doesn't merge — "trust me" is not provenance.

## Scenario packs

New scenarios must state their provenance in `packs/README.md`, in
preference order: verbatim real SDK request > derived from real data by a
documented transformation > hand-written (with justification for why no real
source exists). The `scenario` package's tests enforce pack-level coverage —
if your change removes the only carrier of a parameter, tests go red.

## Code

Standard Go project: `gofmt`, `golangci-lint run` (standard linters plus
`wsl` — config in `.golangci.yml`) and `go test ./...` must pass; CI runs
all three. Keep the loader backward-compatible — existing corpus files
must never stop loading.
