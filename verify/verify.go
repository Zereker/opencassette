// Package verify checks cassettes for the failure modes that make a shared
// corpus untrustworthy. Two of them are existential for a project like
// this, and both were observed in the wild while researching whether such a
// corpus already existed:
//
//   - leaked credentials — a recording that carries a live API key is worse
//     than no recording;
//   - synthetic data passed off as recorded — public repos were found
//     committing hand-written "cassettes" with placeholder ids
//     (chatcmpl-verify-001), epoch-placeholder timestamps (1234567890), and
//     future recorded_at dates, silently defeating the entire point of
//     testing against real traffic.
//
// FAIL findings are objective defects (unparseable file, unscrubbed
// credential header, secret-shaped strings, impossible timestamps) and
// should block a merge. WARN findings are heuristics (placeholder-looking
// ids, inconsistent token accounting, missing provenance) that need a human
// look — a heuristic strong enough to block on would be strong enough for a
// forger to trivially evade, so warns are surfaced, not enforced.
package verify

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/zereker/opencassette/cassette"
)

// Level classifies a finding.
type Level string

const (
	Fail Level = "FAIL"
	Warn Level = "WARN"
)

// Finding is one problem found in one file.
type Finding struct {
	Path  string
	Level Level
	Msg   string
}

const redacted = "**REDACTED**"

// credentialHeaders mirrors the recorder's default scrub set: any of these
// appearing with a value other than **REDACTED** is a leak.
var credentialHeaders = map[string]bool{
	"authorization": true, "proxy-authorization": true,
	"x-api-key": true, "api-key": true, "x-goog-api-key": true, "x-auth-token": true,
	"x-amz-security-token": true,
	"cookie":               true, "set-cookie": true,
}

// High-confidence secret shapes, safe to scan raw file text for (their
// prefixes don't occur in base64 noise at meaningful rates).
var rawSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
	regexp.MustCompile(`ghp_[A-Za-z0-9]{36,}`),
	regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]{10,}`),
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
}

// skKeyPattern is scanned only over decoded bodies and URIs — inside raw
// !!binary base64 runs, `sk-` followed by 20 word characters occurs by
// chance often enough to make raw-text scanning noisy.
var skKeyPattern = regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`)

var (
	placeholderIDPattern = regexp.MustCompile(`(?i)"id"\s*:\s*"[^"]*(verify|placeholder|example|sample|faked?|dummy)[^"]*"`)
	epochPlaceholder     = regexp.MustCompile(`"created"\s*:\s*1234567890\b`)
)

// File runs every check against one cassette.
func File(path string) []Finding {
	return fileAt(path, time.Now())
}

func fileAt(path string, now time.Time) []Finding {
	var out []Finding
	add := func(level Level, format string, args ...any) {
		out = append(out, Finding{Path: path, Level: level, Msg: fmt.Sprintf(format, args...)})
	}

	its, err := cassette.Load(path)
	if err != nil {
		add(Fail, "unparseable: %v", err)
		return out
	}
	if len(its) == 0 {
		add(Fail, "no interactions")
		return out
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		add(Fail, "unreadable: %v", err)
		return out
	}
	for _, pat := range rawSecretPatterns {
		if m := pat.Find(raw); m != nil {
			add(Fail, "secret-shaped string in file: %q", truncate(string(m), 24))
		}
	}

	checkHeaders(raw, add)
	checkMeta(raw, now, add)

	for i, it := range its {
		u, err := url.Parse(it.URI)
		if err != nil || u.Host == "" {
			add(Fail, "interaction #%d: URI %q has no host", i, it.URI)
			continue
		}
		if u.Scheme != "https" {
			add(Warn, "interaction #%d: non-https URI %q (localhost re-recording? real vendor traffic should be https)", i, it.URI)
		}
		for _, body := range [][]byte{it.RequestBody, []byte(it.URI)} {
			if m := skKeyPattern.Find(body); m != nil {
				add(Fail, "interaction #%d: secret-shaped string in request: %q", i, truncate(string(m), 24))
			}
		}
		resp := it.ResponseBody
		if m := skKeyPattern.Find(resp); m != nil {
			add(Fail, "interaction #%d: secret-shaped string in response: %q", i, truncate(string(m), 24))
		}
		if placeholderIDPattern.Match(resp) {
			add(Warn, "interaction #%d: response id looks like a placeholder (synthetic data?)", i)
		}
		if epochPlaceholder.Match(resp) {
			add(Warn, "interaction #%d: response created=1234567890 (epoch placeholder — synthetic data?)", i)
		}
		checkUsage(resp, i, add)
	}
	return out
}

// checkHeaders walks both on-disk formats' header maps looking for a
// credential-bearing header whose value survived unscrubbed — and for
// secret-shaped values in ANY header, because a vendor with a nonstandard
// auth header (recorded without -scrub-header) bypasses the recorder's
// name-based scrub list entirely.
func checkHeaders(raw []byte, add func(Level, string, ...any)) {
	var doc map[string]any
	if yaml.Unmarshal(raw, &doc) != nil {
		return // cassette.Load already parsed it; a divergence here isn't the check's job
	}
	inspect := func(section any) {
		m, ok := section.(map[string]any)
		if !ok {
			return
		}
		headers, ok := m["headers"].(map[string]any)
		if !ok {
			return
		}
		for name, vals := range headers {
			isCredential := credentialHeaders[strings.ToLower(name)]
			check := func(s string) {
				if isCredential {
					if s != redacted {
						add(Fail, "credential header %q is not scrubbed", name)
					}
					return
				}
				if m := skKeyPattern.FindString(s); m != "" {
					add(Fail, "header %q carries a secret-shaped value: %q (nonstandard auth header? record with -scrub-header)", name, truncate(m, 24))
				}
			}
			switch v := vals.(type) {
			case []any:
				for _, item := range v {
					if s, ok := item.(string); ok {
						check(s)
					}
				}
			case string:
				// Hand-written files sometimes carry the value as a scalar
				// instead of the usual one-element list; a leak is a leak.
				check(v)
			}
		}
	}
	if interactions, ok := doc["interactions"].([]any); ok {
		for _, item := range interactions {
			if m, ok := item.(map[string]any); ok {
				inspect(m["request"])
				inspect(m["response"])
			}
		}
	}
	for _, key := range []string{"requests", "responses"} {
		if list, ok := doc[key].([]any); ok {
			for _, item := range list {
				inspect(item)
			}
		}
	}
}

// checkMeta validates the provenance block: absence is a warning (imported
// third-party captures predate the convention), an impossible timestamp is
// a hard failure — a future or pre-LLM-era recorded_at is exactly the
// synthetic-data tell observed in the wild.
func checkMeta(raw []byte, now time.Time, add func(Level, string, ...any)) {
	var doc struct {
		Meta *struct {
			RecordedAt string `yaml:"recorded_at"`
		} `yaml:"meta"`
	}
	if yaml.Unmarshal(raw, &doc) != nil || doc.Meta == nil {
		add(Warn, "no meta provenance block (self-recorded cassettes must carry one)")
		return
	}
	ts, err := time.Parse(time.RFC3339, doc.Meta.RecordedAt)
	if err != nil {
		add(Fail, "meta.recorded_at %q is not RFC3339", doc.Meta.RecordedAt)
		return
	}
	if ts.After(now.Add(24 * time.Hour)) {
		add(Fail, "meta.recorded_at %q is in the future", doc.Meta.RecordedAt)
	}
	if ts.Year() < 2020 {
		add(Fail, "meta.recorded_at %q predates the APIs being recorded", doc.Meta.RecordedAt)
	}
}

// checkUsage flags OpenAI-style token accounting that doesn't add up — real
// upstreams are consistent; hand-typed numbers often aren't. Both plain
// JSON bodies and SSE streams are checked: a forged stream's usage chunk
// is exactly as likely to have hand-typed numbers as a forged JSON body.
func checkUsage(resp []byte, i int, add func(Level, string, ...any)) {
	if checkUsageJSON(resp, i, add) {
		return
	}
	// Not a single JSON document — try SSE: each `data: {...}` line is a
	// candidate chunk, and usage rides in whichever chunk carries it.
	for _, line := range strings.Split(string(resp), "\n") {
		payload, ok := strings.CutPrefix(strings.TrimRight(line, "\r"), "data:")
		if !ok {
			continue
		}
		checkUsageJSON([]byte(strings.TrimSpace(payload)), i, add)
	}
}

// checkUsageJSON reports whether body parsed as JSON (regardless of
// whether it carried usage), warning when usage arithmetic is off.
func checkUsageJSON(body []byte, i int, add func(Level, string, ...any)) bool {
	var probe struct {
		Usage *struct {
			Prompt     *int `json:"prompt_tokens"`
			Completion *int `json:"completion_tokens"`
			Total      *int `json:"total_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &probe) != nil {
		return false
	}
	if u := probe.Usage; u != nil && u.Prompt != nil && u.Completion != nil && u.Total != nil && *u.Prompt+*u.Completion != *u.Total {
		add(Warn, "interaction #%d: usage does not add up (%d + %d != %d) — synthetic data?", i, *u.Prompt, *u.Completion, *u.Total)
	}
	return true
}

// Dir verifies every *.yaml / *.yaml.gz under dir recursively, returning
// all findings plus the number of files examined.
func Dir(dir string) ([]Finding, int, error) {
	var findings []Finding
	files := 0
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !(strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yaml.gz")) {
			return nil
		}
		files++
		findings = append(findings, File(path)...)
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return findings, files, nil
}

// HasFailures reports whether any finding is a hard FAIL.
func HasFailures(findings []Finding) bool {
	for _, f := range findings {
		if f.Level == Fail {
			return true
		}
	}
	return false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
