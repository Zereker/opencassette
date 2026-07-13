package cli

import (
	"context"
	"encoding/json"
	"fmt"

	"golang.org/x/oauth2/google"
	"golang.org/x/oauth2/jwt"
)

// googleServiceAccountToken exchanges a GCP service-account JSON (the
// client_email + private_key pair Vertex AI needs) for a short-lived OAuth2
// access token, used as a bearer credential. The service-account material is
// supplied via RECORD_API_KEY like any other secret; only the resulting token
// reaches the request, and it is registered for redaction so it never lands
// in a cassette. The token is valid ~1h — long enough for one pack run.
func googleServiceAccountToken(saJSON string) (string, error) {
	var sa struct {
		ClientEmail  string `json:"client_email"`
		PrivateKey   string `json:"private_key"`
		PrivateKeyID string `json:"private_key_id"`
		TokenURI     string `json:"token_uri"`
	}

	if err := json.Unmarshal([]byte(saJSON), &sa); err != nil {
		return "", fmt.Errorf("parse service-account JSON: %w", err)
	}

	if sa.ClientEmail == "" || sa.PrivateKey == "" {
		return "", fmt.Errorf("service-account JSON missing client_email or private_key")
	}

	tokenURL := sa.TokenURI
	if tokenURL == "" {
		tokenURL = google.JWTTokenURL
	}

	cfg := &jwt.Config{
		Email:        sa.ClientEmail,
		PrivateKey:   []byte(sa.PrivateKey),
		PrivateKeyID: sa.PrivateKeyID,
		Scopes:       []string{"https://www.googleapis.com/auth/cloud-platform"},
		TokenURL:     tokenURL,
	}

	tok, err := cfg.TokenSource(context.Background()).Token()
	if err != nil {
		return "", fmt.Errorf("fetch access token: %w", err)
	}

	return tok.AccessToken, nil
}
