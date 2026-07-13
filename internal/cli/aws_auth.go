package cli

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// awsBedrockAuth carries the temporary STS credentials plus the metadata
// (region + inference-profile ARN) needed to route and sign a Bedrock invoke
// request.
type awsBedrockAuth struct {
	creds  aws.Credentials
	region string
	arn    string
	signer *v4.Signer
}

type bedrockMetadata struct {
	Region              string `json:"region"`
	RoleARN             string `json:"role_arn"`
	InferenceProfileARN string `json:"inference_profile_arn"`
	AWSKeys             []struct {
		AccessKeyID     string `json:"aws_access_key_id"`
		SecretAccessKey string `json:"aws_secret_access_key"`
	} `json:"aws_keys"`
}

// newAWSBedrockAuth parses the Bedrock service-endpoint metadata, assumes the
// configured role via STS, and returns an auth ready to sign requests plus the
// secret values (long-term and temporary keys) to register for redaction.
func newAWSBedrockAuth(metadataJSON string) (*awsBedrockAuth, []string, error) {
	var m bedrockMetadata
	if err := json.Unmarshal([]byte(metadataJSON), &m); err != nil {
		return nil, nil, fmt.Errorf("parse bedrock metadata: %w", err)
	}

	if m.Region == "" || m.RoleARN == "" || m.InferenceProfileARN == "" || len(m.AWSKeys) == 0 {
		return nil, nil, fmt.Errorf("bedrock metadata needs region, role_arn, inference_profile_arn and aws_keys")
	}

	ak := m.AWSKeys[0].AccessKeyID
	sk := m.AWSKeys[0].SecretAccessKey

	if ak == "" || sk == "" {
		return nil, nil, fmt.Errorf("bedrock metadata aws_keys[0] missing access/secret key")
	}

	cfg := aws.Config{
		Region:      m.Region,
		Credentials: credentials.NewStaticCredentialsProvider(ak, sk, ""),
	}

	out, err := sts.NewFromConfig(cfg).AssumeRole(context.Background(), &sts.AssumeRoleInput{
		RoleArn:         aws.String(m.RoleARN),
		RoleSessionName: aws.String("opencassette-bedrock"),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("STS AssumeRole: %w", err)
	}

	temp := aws.Credentials{
		AccessKeyID:     aws.ToString(out.Credentials.AccessKeyId),
		SecretAccessKey: aws.ToString(out.Credentials.SecretAccessKey),
		SessionToken:    aws.ToString(out.Credentials.SessionToken),
	}

	auth := &awsBedrockAuth{
		creds:  temp,
		region: m.Region,
		arn:    m.InferenceProfileARN,
		signer: v4.NewSigner(),
	}

	secrets := []string{ak, sk, temp.AccessKeyID, temp.SecretAccessKey, temp.SessionToken}

	return auth, secrets, nil
}

// transport returns an HTTP transport whose TLS ServerName is the real Bedrock
// runtime host, so a request to the NLB DNS name still passes certificate
// verification (which is NOT disabled).
func (a *awsBedrockAuth) transport() http.RoundTripper {
	return &http.Transport{
		TLSClientConfig: &tls.Config{
			ServerName: "bedrock-runtime." + a.region + ".amazonaws.com",
		},
		ForceAttemptHTTP2: true,
	}
}

// buildRequest constructs and signs the Bedrock invoke request: the URL is
// {base}/model/{ARN}/invoke and the body has model/stream stripped (the model
// is identified by the ARN in the path) with anthropic_version injected.
func (a *awsBedrockAuth) buildRequest(base string, body []byte) (*http.Request, error) {
	nb, err := rewriteBedrockBody(body)
	if err != nil {
		return nil, err
	}

	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil {
		return nil, fmt.Errorf("parse --url: %w", err)
	}

	// The ARN is one path segment. url.PathEscape encodes its "/" to %2F and
	// leaves ":"; RawPath carries that spelling, and the SDK signer re-encodes
	// it for the canonical request (Bedrock's expected double encoding).
	u.Path = "/model/" + a.arn + "/invoke"
	u.RawPath = "/model/" + url.PathEscape(a.arn) + "/invoke"

	req, err := http.NewRequest(http.MethodPost, u.String(), strings.NewReader(string(nb)))
	if err != nil {
		return nil, err
	}

	req.URL = u
	req.Header.Set("Content-Type", "application/json")

	if err := a.signer.SignHTTP(context.Background(), a.creds, req, hexSHA256(nb), "bedrock", a.region, time.Now()); err != nil {
		return nil, fmt.Errorf("sign bedrock request: %w", err)
	}

	return req, nil
}

// rewriteBedrockBody drops model/stream (the model lives in the URL ARN) and
// injects the Bedrock anthropic_version marker.
func rewriteBedrockBody(body []byte) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("parse request body: %w", err)
	}

	delete(m, "model")
	delete(m, "stream")

	ver, _ := json.Marshal("bedrock-2023-05-31")
	m["anthropic_version"] = ver

	return json.Marshal(m)
}

func hexSHA256(data []byte) string {
	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}
