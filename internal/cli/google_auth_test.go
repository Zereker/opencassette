package cli

import "testing"

// TestGoogleServiceAccountTokenRejectsBadInput covers the validation that
// runs before any network call: malformed JSON or a service account missing
// client_email / private_key must error, not attempt a token exchange.
func TestGoogleServiceAccountTokenRejectsBadInput(t *testing.T) {
	cases := []string{
		"",                          // empty
		"not json",                  // unparseable
		`{"client_email":"a@b.com"}`, // no private_key
		`{"private_key":"x"}`,        // no client_email
	}

	for _, in := range cases {
		if _, err := googleServiceAccountToken(in); err == nil {
			t.Errorf("expected error for %q, got nil", in)
		}
	}
}
