package oauth

import (
	"errors"
	"fmt"
	"testing"
)

// A refused grant is typed, so callers branch on errors.Is instead of reading
// "reconnect" out of the text; a token endpoint that is merely failing is not.
func TestARefusedGrantIsReconnectRequired(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"400 invalid_grant", &tokenStatusError{code: 400, body: `{"error":"invalid_grant"}`}, true},
		{"401 invalid_grant", &tokenStatusError{code: 401, body: `{"error":"invalid_grant"}`}, true},
		// Wrong developer credentials: reconnecting the account cannot fix it.
		{"401 invalid_client is not", &tokenStatusError{code: 401, body: `{"error":"invalid_client"}`}, false},
		{"invalid_grant in a 2xx body", &tokenErrorResponse{code: "invalid_grant", desc: "revoked"}, true},
		{"wrapped by tokenFor", fmt.Errorf("could not refresh the youtube token: %w",
			&tokenStatusError{code: 400, body: `{"error":"invalid_grant"}`}), true},
		{"a 503 is not", &tokenStatusError{code: 503, body: "busy"}, false},
		{"a 400 for another reason is not", &tokenStatusError{code: 400, body: `{"error":"invalid_request"}`}, false},
		{"another 2xx error is not", &tokenErrorResponse{code: "temporarily_unavailable"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := errors.Is(tc.err, ErrReconnectRequired); got != tc.want {
				t.Errorf("errors.Is(%v, ErrReconnectRequired) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// The wording of both errors is unchanged, so logs and the text built on them
// read as before.
func TestTokenErrorWordingIsUnchanged(t *testing.T) {
	if got := (&tokenErrorResponse{code: "invalid_grant", desc: "revoked"}).Error(); got != "invalid_grant: revoked" {
		t.Errorf("Error() = %q", got)
	}
}
