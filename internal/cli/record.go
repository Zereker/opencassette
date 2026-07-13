package cli

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/tidwall/gjson"

	"github.com/zereker/opencassette/internal/recorder"
	"github.com/zereker/opencassette/internal/redact"
	"github.com/zereker/opencassette/internal/scenario"
)

type recordOptions struct {
	endpoint       string
	bodyFile       string
	scenarioDir    string
	probeFields    string
	corpusDir      string
	vendor         string
	model          string
	protocol       string
	name           string
	bucket         string
	out            string
	authStyle      string
	keyEnv         string
	profileDir     string
	appendExisting bool
	force          bool
	timeout        time.Duration
	pause          time.Duration
	headers        []string
	scrubHeaders   []string
}

// runConfig carries the per-run recording knobs shared by every mode. rules
// is built once (baseline + vendor profile + key + one-off scrub headers) and
// shared read-only across every scenario's recorder; each recorder derives
// its own Scrubber, so trace numbering restarts per cassette.
type runConfig struct {
	endpoint      string
	authStyle     string
	key           string
	headers       []string
	rules         *redact.Rules
	baseTransport http.RoundTripper
	awsAuth       *awsBedrockAuth
	timeout       time.Duration
	pause         time.Duration
	force         bool
	stderr        io.Writer
	version       string
}

// newRecorder builds a recording transport over the shared ruleset, wrapping
// the run's base transport (nil = default; aws-sigv4 supplies a TLS-SNI one).
func (c runConfig) newRecorder() *recorder.Recorder {
	return recorder.NewWithRules(c.baseTransport, c.rules)
}

// writeCassette refuses to clobber an existing recording unless the run
// says --force: a file already in the corpus may be human-reviewed, and a
// re-run must not silently replace it.
func (c runConfig) writeCassette(rec *recorder.Recorder, path string) error {
	if !c.force {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%s already exists (re-record with --force)", path)
		}
	}

	return rec.WriteFile(path)
}

func newRecordCommand(app *application) *cobra.Command {
	opts := recordOptions{
		corpusDir: "corpus",
		bucket:     "auto",
		authStyle:  "bearer",
		keyEnv:     "RECORD_API_KEY",
		profileDir: "profiles",
		timeout:    3 * time.Minute,
		pause:      time.Second,
	}
	cmd := &cobra.Command{
		Use:   "record",
		Short: "Record real API calls into scrubbed cassettes",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runRecordCommand(app, opts)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&opts.endpoint, "url", "", "full endpoint URL (required)")
	flags.StringVar(&opts.bodyFile, "body-file", "", "request body file, '-' for stdin (single-recording mode)")
	flags.StringVar(&opts.scenarioDir, "scenario-dir", "", "record every scenario in this pack")
	flags.StringVar(&opts.probeFields, "probe-fields", "", "probe per-field vendor support using this pack")
	flags.StringVar(&opts.corpusDir, "corpus-dir", opts.corpusDir, "corpus root the output path is composed under")
	flags.StringVar(&opts.vendor, "vendor", "", "vendor directory name, e.g. deepseek / zhipu / minimax")
	flags.StringVar(&opts.model, "model", "", "model directory name, e.g. deepseek-chat / glm-4-plus")
	flags.StringVar(&opts.protocol, "protocol", "", "wire protocol path segment (default: pack manifest, else openai)")
	flags.StringVar(&opts.name, "name", "", "scenario name (file basename without .yaml)")
	flags.StringVar(&opts.bucket, "bucket", opts.bucket, "stream | nostream | auto")
	flags.StringVar(&opts.out, "out", "", "explicit output path (overrides composition)")
	flags.StringVar(&opts.authStyle, "auth", opts.authStyle, "bearer | x-api-key | api-key | query:<param> | google-sa | aws-sigv4 | none")
	flags.StringVar(&opts.keyEnv, "key-env", opts.keyEnv, "environment variable holding the API key")
	flags.StringVar(&opts.profileDir, "profile-dir", opts.profileDir, "directory of vendor redaction profiles (<vendor>.yaml)")
	flags.BoolVar(&opts.appendExisting, "append", false, "prepend the existing cassette")
	flags.BoolVar(&opts.force, "force", false, "overwrite existing cassettes")
	flags.DurationVar(&opts.timeout, "timeout", opts.timeout, "request timeout")
	flags.DurationVar(&opts.pause, "pause", opts.pause, "delay between calls in batch mode")
	flags.StringArrayVar(&opts.headers, "header", nil, "extra request header 'Name: value' (repeatable)")
	flags.StringArrayVar(&opts.scrubHeaders, "scrub-header", nil, "additional header name to redact (repeatable)")

	return cmd
}

func runRecordCommand(app *application, opts recordOptions) error {
	if opts.endpoint == "" {
		return fmt.Errorf("record: --url is required")
	}

	if opts.bucket != "auto" && opts.bucket != "stream" && opts.bucket != "nostream" {
		return fmt.Errorf("record: --bucket %q (want stream | nostream | auto)", opts.bucket)
	}

	for _, header := range opts.headers {
		if !strings.Contains(header, ":") {
			return fmt.Errorf("record: --header wants %q, got %q", "Name: value", header)
		}
	}

	opts.endpoint = strings.ReplaceAll(opts.endpoint, "{model}", opts.model)

	key := os.Getenv(opts.keyEnv)
	if key == "" && opts.authStyle != "none" {
		return fmt.Errorf("record: environment variable %s is empty (or pass --auth none)", opts.keyEnv)
	}

	// google-sa resolves a service-account JSON to a short-lived bearer token
	// up front, so the rest of the pipeline just sees --auth bearer.
	if opts.authStyle == "google-sa" {
		token, err := googleServiceAccountToken(key)
		if err != nil {
			return fmt.Errorf("record: google-sa auth: %w", err)
		}

		key = token
		opts.authStyle = "bearer"
	}

	batchMode := opts.scenarioDir != "" || opts.probeFields != ""
	if batchMode {
		if opts.bodyFile != "" || opts.name != "" || opts.out != "" || opts.appendExisting {
			return fmt.Errorf("record: --scenario-dir/--probe-fields are exclusive with --body-file/--name/--out/--append")
		}

		if opts.scenarioDir != "" && opts.probeFields != "" {
			return fmt.Errorf("record: --scenario-dir and --probe-fields are exclusive")
		}

		if opts.vendor == "" || opts.model == "" {
			return fmt.Errorf("record: batch/probe mode needs --vendor and --model")
		}
	}

	profile, err := redact.LoadProfile(opts.profileDir, opts.vendor)
	if err != nil {
		return fmt.Errorf("record: %w", err)
	}

	if profile != nil {
		_, _ = fmt.Fprintf(app.stderr, "redaction profile: %s/%s.yaml\n", opts.profileDir, opts.vendor)
	}

	rules := redact.Baseline()
	if err := rules.Merge(profile); err != nil {
		return fmt.Errorf("record: %w", err)
	}

	rules.AddSecret(key)

	for _, h := range opts.scrubHeaders {
		rules.AddCredentialHeader(h)
	}

	// aws-sigv4: the key is the Bedrock endpoint metadata JSON. Assume the
	// role now, register the resulting credentials for redaction, and route
	// over a TLS-SNI transport (the NLB DNS name doesn't match its cert).
	var awsAuth *awsBedrockAuth
	var baseTransport http.RoundTripper

	if opts.authStyle == "aws-sigv4" {
		auth, secrets, err := newAWSBedrockAuth(key)
		if err != nil {
			return fmt.Errorf("record: aws-sigv4: %w", err)
		}

		awsAuth = auth
		baseTransport = auth.transport()

		for _, s := range secrets {
			rules.AddSecret(s)
		}
	}

	run := runConfig{
		endpoint: opts.endpoint, authStyle: opts.authStyle, key: key,
		headers: opts.headers, rules: rules,
		baseTransport: baseTransport, awsAuth: awsAuth,
		timeout: opts.timeout, pause: opts.pause, force: opts.force,
		stderr: app.stderr, version: app.version,
	}
	if opts.probeFields != "" {
		return runProbe(run, opts.probeFields, opts.corpusDir, opts.vendor, opts.model, protocolOr(opts.protocol, "openai"))
	}

	if opts.scenarioDir != "" {
		return runBatch(run, opts.scenarioDir, opts.corpusDir, opts.vendor, opts.model, opts.protocol, opts.bucket)
	}

	if opts.bodyFile == "" {
		return fmt.Errorf("record: --body-file (or --scenario-dir) is required")
	}

	body, err := readBody(opts.bodyFile, app.stdin)
	if err != nil {
		return fmt.Errorf("record: read body: %w", err)
	}

	stream := bucketStream(opts.bucket, gjson.GetBytes(body, "stream").Bool())

	outPath, err := resolveOutPath(opts.out, opts.corpusDir, opts.vendor, opts.model, protocolOr(opts.protocol, "openai"), opts.name, stream, opts.appendExisting)
	if err != nil {
		return fmt.Errorf("record: %w", err)
	}

	if err := recordOne(run, body, outPath, opts.appendExisting, metaFor(opts.endpoint, opts.vendor, opts.model, opts.name, "", app.version)); err != nil {
		return fmt.Errorf("record: %w", err)
	}

	_, _ = fmt.Fprintln(app.stderr, "before publishing: read the file, then run `opencassette verify` over it")

	return nil
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

func runBatch(run runConfig, dir, corpusDir, vendor, model, protocol, bucket string) error {
	pack, err := scenario.LoadPack(dir)
	if err != nil {
		return fmt.Errorf("record: %w", err)
	}

	protocol = protocolOr(protocol, pack.Protocol)
	if pack.ModelField == "" && !strings.Contains(run.endpoint, model) {
		return fmt.Errorf("record: this pack carries no model in the body — put a {model} placeholder in --url")
	}

	if pack.StreamField == "" && bucket == "auto" {
		return fmt.Errorf("record: this pack's bodies don't signal streaming — pass --bucket stream or --bucket nostream")
	}

	var failed []string

	for i, sc := range pack.Scenarios {
		if i > 0 {
			time.Sleep(run.pause)
		}

		_, _ = fmt.Fprintf(run.stderr, "\n===== scenario %d/%d: %s =====\n", i+1, len(pack.Scenarios), sc.Name)

		body, err := sc.WithModel(model)
		if err != nil {
			return fmt.Errorf("record: %w", err)
		}

		outPath, err := resolveOutPath("", corpusDir, vendor, model, protocol, sc.Name, bucketStream(bucket, sc.Stream), false)
		if err != nil {
			return fmt.Errorf("record: %w", err)
		}

		if err := recordOne(run, body, outPath, false, metaFor(run.endpoint, vendor, model, sc.Name, sc.SHA256(), run.version)); err != nil {
			_, _ = fmt.Fprintf(run.stderr, "SKIPPED %s: %v\n", sc.Name, err)
			failed = append(failed, sc.Name)
		}
	}

	_, _ = fmt.Fprintf(run.stderr, "\n%d/%d scenarios recorded\n", len(pack.Scenarios)-len(failed), len(pack.Scenarios))

	if len(failed) > 0 {
		_, _ = fmt.Fprintf(run.stderr, "failed: %s\n", strings.Join(failed, ", "))
	}

	_, _ = fmt.Fprintln(run.stderr, "before publishing: read the files, then run `opencassette verify` over them")

	if len(failed) > 0 {
		return exitCodeError{code: 1}
	}

	return nil
}

// =============================================================================
// probe-fields
// =============================================================================

// metaFor builds the provenance block; scenarioSHA is the pack file's hash
// in batch mode (or the field-source file's in probe mode), empty for
// ad-hoc -body-file recordings, which have no pack version to trace to.
func metaFor(endpoint, vendor, model, scenarioName, scenarioSHA, version string) recorder.Meta {
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

func recordOne(run runConfig, body []byte, outPath string, appendExisting bool, meta recorder.Meta) error {
	rec := run.newRecorder()
	if appendExisting {
		if err := rec.PrependFromFile(outPath); err != nil {
			return fmt.Errorf("-append: %w", err)
		}
	} else {
		rec.SetMeta(meta)
		// Check before spending the API call, not after.
		if !run.force {
			if _, err := os.Stat(outPath); err == nil {
				return fmt.Errorf("%s already exists (re-record with -force)", outPath)
			}
		}
	}

	req, err := buildRequest(run, body, rec)
	if err != nil {
		return err
	}

	client := &http.Client{Transport: rec, Timeout: run.timeout}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed (nothing recorded): %w", err)
	}

	respBody, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	_, _ = fmt.Fprintf(run.stderr, "HTTP %s\n%s\n", resp.Status, preview(respBody, 2000))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// A real error response is real data too — but make the operator read
		// it and re-run with the request fixed, rather than silently
		// publishing an error cassette.
		return fmt.Errorf("upstream returned %s; not writing %s (fix the request and re-run)", resp.Status, outPath)
	}

	if appendExisting {
		if err := rec.WriteFile(outPath); err != nil {
			return err
		}
	} else if err := run.writeCassette(rec, outPath); err != nil {
		return err
	}

	_, _ = fmt.Fprintf(run.stderr, "wrote %d interaction(s) to %s\n", rec.Len(), outPath)

	return nil
}

func readBody(path string, stdin io.Reader) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(stdin)
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

func buildRequest(run runConfig, body []byte, rec *recorder.Recorder) (*http.Request, error) {
	if run.authStyle == "aws-sigv4" {
		return run.awsAuth.buildRequest(run.endpoint, body)
	}

	finalURL := run.endpoint
	if param, ok := strings.CutPrefix(run.authStyle, "query:"); ok {
		u, err := url.Parse(run.endpoint)
		if err != nil {
			return nil, fmt.Errorf("parse -url: %w", err)
		}

		q := u.Query()
		q.Set(param, run.key)
		u.RawQuery = q.Encode()
		finalURL = u.String()

		rec.ScrubQueryParam(param)
	}

	req, err := http.NewRequest(http.MethodPost, finalURL, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")

	for _, h := range run.headers {
		name, value, _ := strings.Cut(h, ":")
		req.Header.Set(strings.TrimSpace(name), strings.TrimSpace(value))
	}

	switch {
	case run.authStyle == "bearer":
		req.Header.Set("Authorization", "Bearer "+run.key)
	case run.authStyle == "x-api-key":
		req.Header.Set("x-api-key", run.key)
	case run.authStyle == "api-key":
		req.Header.Set("api-key", run.key)
	case run.authStyle == "none", strings.HasPrefix(run.authStyle, "query:"):
		// handled above / nothing to add
	default:
		return nil, fmt.Errorf("unknown --auth %q (want bearer | x-api-key | api-key | query:<param> | none)", run.authStyle)
	}

	return req, nil
}

func preview(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}

	return string(b[:n]) + fmt.Sprintf("\n...(truncated, %d bytes total)", len(b))
}
