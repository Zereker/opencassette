package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
)

// bedrockStreamTransport decodes a Bedrock invoke-with-response-stream
// response — an AWS eventstream of binary frames — into a readable SSE body
// before the recorder captures it. Each frame's payload is {"bytes": base64}
// wrapping one Anthropic streaming event; it is unwrapped to a
// `data: <event>\n\n` line. Only the binary transport envelope is removed —
// the event JSON inside is the real model output, unchanged.
type bedrockStreamTransport struct {
	base http.RoundTripper
}

func (t *bedrockStreamTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return resp, err
	}

	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Type"), "eventstream") {
		return resp, nil
	}

	decoded, derr := decodeBedrockEventstream(resp.Body)
	_ = resp.Body.Close()

	if derr != nil {
		return nil, fmt.Errorf("decode bedrock eventstream: %w", derr)
	}

	resp.Body = io.NopCloser(bytes.NewReader(decoded))
	resp.Header.Set("Content-Type", "text/event-stream")
	resp.Header.Del("Content-Length")
	resp.ContentLength = int64(len(decoded))

	return resp, nil
}

// decodeBedrockEventstream reads eventstream frames and emits each frame's
// unwrapped Anthropic event as an SSE `data:` line.
func decodeBedrockEventstream(r io.Reader) ([]byte, error) {
	dec := eventstream.NewDecoder()

	var (
		out     bytes.Buffer
		payload []byte
	)

	for {
		msg, err := dec.Decode(r, payload)
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return nil, err
		}

		var wrap struct {
			Bytes []byte `json:"bytes"`
		}

		if json.Unmarshal(msg.Payload, &wrap) == nil && len(wrap.Bytes) > 0 {
			out.WriteString("data: ")
			out.Write(wrap.Bytes)
			out.WriteString("\n\n")
		}

		payload = msg.Payload[:0]
	}

	return out.Bytes(), nil
}
