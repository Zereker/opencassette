// Command opencassette is the CLI over this repo's library packages:
//
//	opencassette record  — make real API calls and write scrubbed cassettes
//	opencassette verify  — check a corpus for leaks and synthetic-data tells
//
// Recording one scenario:
//
//	echo '{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}]}' > /tmp/req.json
//	RECORD_API_KEY=sk-... opencassette record \
//	  -url https://api.deepseek.com/chat/completions \
//	  -body-file /tmp/req.json \
//	  -vendor deepseek -model deepseek-chat -name chat_basic
//	# -> corpus/deepseek/deepseek-chat/openai/nostream/chat_basic.yaml
//
// Recording a whole scenario pack (one cassette per scenario):
//
//	RECORD_API_KEY=sk-... opencassette record \
//	  -url https://api.deepseek.com/chat/completions \
//	  -scenario-dir packs/openai-chat \
//	  -vendor deepseek -model deepseek-chat
//
// Probing per-field vendor support (one minimal request per top-level field
// of the pack's chat_full_params.json, plus synthetic probes like
// stream_options that the full body can't legally carry; accepted fields'
// cassettes land in fields/, 400/422 rejections — evidence of non-support —
// in fields-rejected/, and a field-support.json matrix alongside):
//
//	RECORD_API_KEY=sk-... opencassette record \
//	  -url https://api.deepseek.com/chat/completions \
//	  -probe-fields packs/openai-chat \
//	  -vendor deepseek -model deepseek-chat
//
// The API key is read from an environment variable (default RECORD_API_KEY),
// never from a flag; it is scrubbed from recordings both by header name and
// by literal value. Auth styles (-auth): bearer (default) | x-api-key |
// api-key | query:<param> | none. A scenario the upstream rejects (non-2xx)
// is skipped and reported, never written.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"github.com/zereker/opencassette/recorder"
	"github.com/zereker/opencassette/scenario"
	"github.com/zereker/opencassette/verify"
)

const version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "record":
		runRecord(os.Args[2:])
	case "verify":
		runVerify(os.Args[2:])
	case "version", "-version", "--version":
		fmt.Println("opencassette/" + version)
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: opencassette <record|verify> [flags]  (see -h on each subcommand)")
	os.Exit(2)
}

// =============================================================================
// verify
// =============================================================================

func runVerify(args []string) {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	dir := fs.String("dir", "corpus", "corpus directory to verify")
	_ = fs.Parse(args)
	if fs.NArg() > 0 {
		*dir = fs.Arg(0)
	}

	findings, files, err := verify.Dir(*dir)
	if err != nil {
		log.Fatalf("verify: %v", err)
	}
	for _, f := range findings {
		fmt.Printf("%s  %s: %s\n", f.Level, f.Path, f.Msg)
	}
	fmt.Printf("%d file(s) verified, %d finding(s)\n", files, len(findings))
	if verify.HasFailures(findings) {
		os.Exit(1)
	}
}

// =============================================================================
// record
// =============================================================================

type headerFlags []string

func (h *headerFlags) String() string { return strings.Join(*h, "; ") }
func (h *headerFlags) Set(v string) error {
	if !strings.Contains(v, ":") {
		return fmt.Errorf("want \"Name: value\", got %q", v)
	}
	*h = append(*h, v)
	return nil
}

func runRecord(args []string) {
	fs := flag.NewFlagSet("record", flag.ExitOnError)
	endpoint := fs.String("url", "", "full endpoint URL (required)")
	bodyFile := fs.String("body-file", "", "request body file, '-' for stdin (single-recording mode)")
	scenarioDir := fs.String("scenario-dir", "", "record every scenario in this pack instead of one -body-file")
	probeFields := fs.String("probe-fields", "", "probe per-field vendor support using this pack dir's chat_basic.json (base) + chat_full_params.json (field source); writes fields/, fields-rejected/ and field-support.json")
	corpusDir := fs.String("corpus-dir", "corpus", "corpus root the output path is composed under")
	vendor := fs.String("vendor", "", "vendor directory name, e.g. deepseek / zhipu / minimax")
	model := fs.String("model", "", "model directory name, e.g. deepseek-chat / glm-4-plus")
	protocol := fs.String("protocol", "", "wire protocol path segment (default: the pack manifest's, else openai)")
	name := fs.String("name", "", "scenario name (file basename without .yaml)")
	bucket := fs.String("bucket", "auto", "stream|nostream|auto — corpus bucket; auto reads the body's stream field, but URL-streamed protocols (Gemini :streamGenerateContent) need it set explicitly")
	out := fs.String("out", "", "explicit output path (overrides composition)")
	authStyle := fs.String("auth", "bearer", "bearer | x-api-key | api-key | query:<param> | none")
	keyEnv := fs.String("key-env", "RECORD_API_KEY", "environment variable holding the API key")
	appendExisting := fs.Bool("append", false, "prepend the existing cassette (must have been written by this tool)")
	timeout := fs.Duration("timeout", 3*time.Minute, "request timeout (reasoning models can be slow)")
	pause := fs.Duration("pause", time.Second, "delay between scenario calls in batch mode")
	var headers headerFlags
	fs.Var(&headers, "header", "extra request header \"Name: value\" (repeatable)")
	_ = fs.Parse(args)

	if *endpoint == "" {
		log.Fatal("record: -url is required")
	}
	if *bucket != "auto" && *bucket != "stream" && *bucket != "nostream" {
		log.Fatalf("record: -bucket %q (want stream | nostream | auto)", *bucket)
	}
	// The model may live in the URL path rather than the body (Gemini's
	// /models/<model>:generateContent) — substitute a {model} placeholder.
	*endpoint = strings.ReplaceAll(*endpoint, "{model}", *model)
	key := os.Getenv(*keyEnv)
	if key == "" && *authStyle != "none" {
		log.Fatalf("record: environment variable %s is empty (or pass -auth none)", *keyEnv)
	}

	if *scenarioDir != "" || *probeFields != "" {
		if *bodyFile != "" || *name != "" || *out != "" || *appendExisting {
			log.Fatal("record: -scenario-dir/-probe-fields are exclusive with -body-file/-name/-out/-append")
		}
		if *scenarioDir != "" && *probeFields != "" {
			log.Fatal("record: -scenario-dir and -probe-fields are exclusive (run them as separate passes)")
		}
		if *vendor == "" || *model == "" {
			log.Fatal("record: batch/probe mode needs -vendor and -model")
		}
		if *probeFields != "" {
			runProbe(*probeFields, *corpusDir, *endpoint, *vendor, *model, protocolOr(*protocol, "openai"), *authStyle, key, headers, *timeout, *pause)
		} else {
			runBatch(*scenarioDir, *corpusDir, *endpoint, *vendor, *model, *protocol, *bucket, *authStyle, key, headers, *timeout, *pause)
		}
		return
	}

	if *bodyFile == "" {
		log.Fatal("record: -body-file (or -scenario-dir) is required")
	}
	body, err := readBody(*bodyFile)
	if err != nil {
		log.Fatalf("record: read body: %v", err)
	}
	stream := bucketStream(*bucket, gjson.GetBytes(body, "stream").Bool())
	outPath, err := resolveOutPath(*out, *corpusDir, *vendor, *model, protocolOr(*protocol, "openai"), *name, stream, *appendExisting)
	if err != nil {
		log.Fatalf("record: %v", err)
	}
	if err := recordOne(*endpoint, body, *authStyle, key, headers, *timeout, outPath, *appendExisting, metaFor(*endpoint, *vendor, *model, *name, "")); err != nil {
		log.Fatalf("record: %v", err)
	}
	fmt.Fprintln(os.Stderr, "before publishing: read the file, then run `opencassette verify` over it")
}

// protocolOr resolves the corpus protocol segment: the explicit -protocol
// flag wins, then fallback (a pack manifest's protocol, or "openai").
func protocolOr(flagValue, fallback string) string {
	if flagValue != "" {
		return flagValue
	}
	return fallback
}

// bucketStream folds the -bucket override into the body-derived stream flag.
func bucketStream(bucket string, bodyStream bool) bool {
	switch bucket {
	case "stream":
		return true
	case "nostream":
		return false
	}
	return bodyStream
}

func runBatch(dir, corpusDir, endpoint, vendor, model, protocol, bucket, authStyle, key string, headers headerFlags, timeout, pause time.Duration) {
	pack, err := scenario.LoadPack(dir)
	if err != nil {
		log.Fatalf("record: %v", err)
	}
	protocol = protocolOr(protocol, pack.Protocol)
	if pack.ModelField == "" && !strings.Contains(endpoint, model) {
		log.Fatalf("record: this pack carries no model in the body — put a {model} placeholder in -url (e.g. .../models/{model}:generateContent)")
	}
	if pack.StreamField == "" && bucket == "auto" {
		log.Fatalf("record: this pack's bodies don't signal streaming (the endpoint does) — pass -bucket stream or -bucket nostream")
	}
	var failed []string
	for i, sc := range pack.Scenarios {
		if i > 0 {
			time.Sleep(pause)
		}
		fmt.Fprintf(os.Stderr, "\n===== scenario %d/%d: %s =====\n", i+1, len(pack.Scenarios), sc.Name)
		body, err := sc.WithModel(model)
		if err != nil {
			log.Fatalf("record: %v", err)
		}
		outPath, err := resolveOutPath("", corpusDir, vendor, model, protocol, sc.Name, bucketStream(bucket, sc.Stream), false)
		if err != nil {
			log.Fatalf("record: %v", err)
		}
		if err := recordOne(endpoint, body, authStyle, key, headers, timeout, outPath, false, metaFor(endpoint, vendor, model, sc.Name, sc.SHA256())); err != nil {
			fmt.Fprintf(os.Stderr, "SKIPPED %s: %v\n", sc.Name, err)
			failed = append(failed, sc.Name)
		}
	}
	fmt.Fprintf(os.Stderr, "\n%d/%d scenarios recorded\n", len(pack.Scenarios)-len(failed), len(pack.Scenarios))
	if len(failed) > 0 {
		fmt.Fprintf(os.Stderr, "failed: %s\n", strings.Join(failed, ", "))
	}
	fmt.Fprintln(os.Stderr, "before publishing: read the files, then run `opencassette verify` over them")
	if len(failed) > 0 {
		os.Exit(1)
	}
}

// =============================================================================
// probe-fields
// =============================================================================

// fieldSupport is the machine-readable support matrix a probe run writes
// next to the fields/ and fields-rejected/ trees: per field, whether the
// vendor accepted it in isolation, with the evidence cassette alongside.
type fieldSupport struct {
	RecordedAt   string                 `json:"recorded_at"`
	Vendor       string                 `json:"vendor"`
	Model        string                 `json:"model"`
	Endpoint     string                 `json:"endpoint"`
	Source       string                 `json:"source"`
	SourceSHA256 string                 `json:"source_sha256"`
	Fields       map[string]fieldResult `json:"fields"`
}

type fieldResult struct {
	Status     string   `json:"status"` // supported | rejected | error
	HTTP       int      `json:"http,omitempty"`
	Companions []string `json:"companions,omitempty"`
}

func runProbe(dir, corpusDir, endpoint, vendor, model, protocol, authStyle, key string, headers headerFlags, timeout, pause time.Duration) {
	baseRaw, err := os.ReadFile(filepath.Join(dir, "chat_basic.json"))
	if err != nil {
		log.Fatalf("record: probe base: %v", err)
	}
	base, err := scenario.Scenario{Name: "chat_basic", Body: baseRaw, ModelField: "model"}.WithModel(model)
	if err != nil {
		log.Fatalf("record: %v", err)
	}
	fullRaw, err := os.ReadFile(filepath.Join(dir, "chat_full_params.json"))
	if err != nil {
		log.Fatalf("record: probe field source: %v", err)
	}
	fullSHA := scenario.Scenario{Body: fullRaw}.SHA256()
	probes, err := scenario.BuildProbes(base, fullRaw)
	if err != nil {
		log.Fatalf("record: %v", err)
	}

	// If the minimal body itself fails, every probe would read as a
	// rejection — abort instead of writing a matrix of noise.
	fmt.Fprintln(os.Stderr, "===== baseline: minimal request =====")
	status, _, err := probeOne(endpoint, base, authStyle, key, headers, timeout, recorder.Meta{})
	if err != nil {
		log.Fatalf("record: baseline call failed (nothing probed): %v", err)
	}
	if status < 200 || status >= 300 {
		log.Fatalf("record: baseline minimal request got HTTP %d — fix endpoint/model/auth before probing fields", status)
	}

	protoDir := filepath.Join(corpusDir, vendor, model, protocol)
	report := fieldSupport{
		RecordedAt:   time.Now().UTC().Format(time.RFC3339),
		Vendor:       vendor,
		Model:        model,
		Endpoint:     hostOf(endpoint),
		Source:       "chat_full_params.json",
		SourceSHA256: fullSHA,
		Fields:       map[string]fieldResult{},
	}
	var errored []string
	for _, p := range probes {
		time.Sleep(pause)
		fmt.Fprintf(os.Stderr, "\n===== field %s =====\n", p.Field)
		res := fieldResult{Companions: p.Companions}
		if strings.ContainsAny(p.Field, `/\`) {
			fmt.Fprintf(os.Stderr, "ERROR: field name %q is not a path segment\n", p.Field)
			res.Status = "error"
			report.Fields[p.Field] = res
			errored = append(errored, p.Field)
			continue
		}
		meta := metaFor(endpoint, vendor, model, "field:"+p.Field, fullSHA)
		status, rec, err := probeOne(endpoint, p.Body, authStyle, key, headers, timeout, meta)
		res.HTTP = status
		switch {
		case err != nil:
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			res.Status = "error"
			errored = append(errored, p.Field)
		case status >= 200 && status < 300:
			res.Status = "supported"
			path := filepath.Join(protoDir, "fields", p.Field+".yaml")
			if err := rec.WriteFile(path); err != nil {
				log.Fatalf("record: %v", err)
			}
			fmt.Fprintf(os.Stderr, "SUPPORTED — wrote %s\n", path)
		case status == 400 || status == 422:
			res.Status = "rejected"
			path := filepath.Join(protoDir, "fields-rejected", p.Field+".yaml")
			if err := rec.WriteFile(path); err != nil {
				log.Fatalf("record: %v", err)
			}
			fmt.Fprintf(os.Stderr, "REJECTED — wrote %s\n", path)
		default:
			// 401/403/429/5xx say nothing about the field itself: record
			// no evidence, claim neither support nor rejection.
			res.Status = "error"
			errored = append(errored, fmt.Sprintf("%s (HTTP %d)", p.Field, status))
		}
		report.Fields[p.Field] = res
	}

	if err := os.MkdirAll(protoDir, 0o755); err != nil {
		log.Fatalf("record: mkdir %s: %v", protoDir, err)
	}
	buf, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		log.Fatalf("record: marshal support matrix: %v", err)
	}
	reportPath := filepath.Join(protoDir, "field-support.json")
	if err := os.WriteFile(reportPath, append(buf, '\n'), 0o644); err != nil {
		log.Fatalf("record: write %s: %v", reportPath, err)
	}

	supported, rejected := 0, 0
	for _, r := range report.Fields {
		switch r.Status {
		case "supported":
			supported++
		case "rejected":
			rejected++
		}
	}
	fmt.Fprintf(os.Stderr, "\n%d field(s): %d supported, %d rejected, %d error(s) — matrix in %s\n",
		len(probes), supported, rejected, len(errored), reportPath)
	if len(errored) > 0 {
		fmt.Fprintf(os.Stderr, "no evidence recorded for: %s\n", strings.Join(errored, ", "))
	}
	fmt.Fprintln(os.Stderr, "before publishing: read the files, then run `opencassette verify` over them")
	if len(errored) > 0 {
		os.Exit(1)
	}
}

// probeOne sends one probe request through a fresh recorder and reports the
// upstream status; the caller decides which bucket (if any) the recording
// lands in — unlike recordOne, a 4xx here is data, not an operator mistake.
func probeOne(endpoint string, body []byte, authStyle, key string, headers headerFlags, timeout time.Duration, meta recorder.Meta) (int, *recorder.Recorder, error) {
	rec := recorder.New(nil)
	rec.RedactValue(key)
	rec.SetMeta(meta)
	req, err := buildRequest(endpoint, body, authStyle, key, headers, rec)
	if err != nil {
		return 0, nil, err
	}
	client := &http.Client{Transport: rec, Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	respBody, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return 0, nil, fmt.Errorf("read response: %w", err)
	}
	fmt.Fprintf(os.Stderr, "HTTP %s\n%s\n", resp.Status, preview(respBody, 800))
	return resp.StatusCode, rec, nil
}

// metaFor builds the provenance block; scenarioSHA is the pack file's hash
// in batch mode (or the field-source file's in probe mode), empty for
// ad-hoc -body-file recordings, which have no pack version to trace to.
func metaFor(endpoint, vendor, model, scenarioName, scenarioSHA string) recorder.Meta {
	return recorder.Meta{
		RecordedAt:     time.Now().UTC().Format(time.RFC3339),
		Vendor:         vendor,
		Model:          model,
		Endpoint:       hostOf(endpoint),
		Scenario:       scenarioName,
		ScenarioSHA256: scenarioSHA,
		Tool:           "opencassette/" + version,
	}
}

func hostOf(endpoint string) string {
	if u, err := url.Parse(endpoint); err == nil && u.Host != "" {
		return u.Scheme + "://" + u.Host
	}
	return endpoint
}

func recordOne(endpoint string, body []byte, authStyle, key string, headers headerFlags, timeout time.Duration, outPath string, appendExisting bool, meta recorder.Meta) error {
	rec := recorder.New(nil)
	rec.RedactValue(key)
	if appendExisting {
		if err := rec.PrependFromFile(outPath); err != nil {
			return fmt.Errorf("-append: %w", err)
		}
	} else {
		rec.SetMeta(meta)
	}

	req, err := buildRequest(endpoint, body, authStyle, key, headers, rec)
	if err != nil {
		return err
	}
	client := &http.Client{Transport: rec, Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed (nothing recorded): %w", err)
	}
	respBody, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	fmt.Fprintf(os.Stderr, "HTTP %s\n%s\n", resp.Status, preview(respBody, 2000))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// A real error response is real data too — but make the operator read
		// it and re-run with the request fixed, rather than silently
		// publishing an error cassette.
		return fmt.Errorf("upstream returned %s; not writing %s (fix the request and re-run)", resp.Status, outPath)
	}
	if err := rec.WriteFile(outPath); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "wrote %d interaction(s) to %s\n", rec.Len(), outPath)
	return nil
}

func readBody(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}

// resolveOutPath composes <corpus>/<vendor>/<model>/<protocol>/<stream|nostream>/<name>.yaml
// unless -out overrides it. The caller decides the bucket (body stream
// field, pack manifest, or -bucket override); on -append, if the composed
// bucket has no file but the sibling bucket does, the existing file wins —
// a multi-turn scenario is classified by its first turn, and turn 2 of an
// agent loop is typically non-streaming even when turn 1 streamed.
func resolveOutPath(out, corpusDir, vendor, model, protocol, name string, stream bool, appendExisting bool) (string, error) {
	if out != "" {
		return out, nil
	}
	if vendor == "" || model == "" || name == "" {
		return "", fmt.Errorf("either -out, or all of -vendor/-model/-name, must be set")
	}
	for flagName, v := range map[string]string{"-vendor": vendor, "-model": model, "-protocol": protocol, "-name": name} {
		if strings.ContainsAny(v, `/\`) {
			return "", fmt.Errorf("%s %q must be a single path segment", flagName, v)
		}
	}
	bucket := "nostream"
	if stream {
		bucket = "stream"
	}
	path := filepath.Join(corpusDir, vendor, model, protocol, bucket, name+".yaml")
	if appendExisting {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			sibling := "stream"
			if bucket == "stream" {
				sibling = "nostream"
			}
			alt := filepath.Join(corpusDir, vendor, model, protocol, sibling, name+".yaml")
			if _, err := os.Stat(alt); err == nil {
				return alt, nil
			}
		}
	}
	return path, nil
}

func buildRequest(endpoint string, body []byte, authStyle, key string, headers headerFlags, rec *recorder.Recorder) (*http.Request, error) {
	finalURL := endpoint
	if param, ok := strings.CutPrefix(authStyle, "query:"); ok {
		u, err := url.Parse(endpoint)
		if err != nil {
			return nil, fmt.Errorf("parse -url: %w", err)
		}
		q := u.Query()
		q.Set(param, key)
		u.RawQuery = q.Encode()
		finalURL = u.String()
		rec.ScrubQueryParam(param)
	}

	req, err := http.NewRequest("POST", finalURL, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for _, h := range headers {
		name, value, _ := strings.Cut(h, ":")
		req.Header.Set(strings.TrimSpace(name), strings.TrimSpace(value))
	}
	switch {
	case authStyle == "bearer":
		req.Header.Set("Authorization", "Bearer "+key)
	case authStyle == "x-api-key":
		req.Header.Set("x-api-key", key)
	case authStyle == "api-key":
		req.Header.Set("api-key", key)
	case authStyle == "none", strings.HasPrefix(authStyle, "query:"):
		// handled above / nothing to add
	default:
		return nil, fmt.Errorf("unknown -auth %q (want bearer | x-api-key | api-key | query:<param> | none)", authStyle)
	}
	return req, nil
}

func preview(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + fmt.Sprintf("\n...(truncated, %d bytes total)", len(b))
}
