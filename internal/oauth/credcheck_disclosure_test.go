package oauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

/* THE CLIENT SECRET IS IN THE QUERY STRING, AND A TRANSPORT FAILURE ECHOES IT.
 *
 * Facebook's CheckCredentials sends the pair as GET query parameters, and that
 * shape is load-bearing: the comment above it records that reusing postForm
 * makes Facebook answer 400 for a CORRECT pair, so every good credential would
 * be reported as rejected. The GET stays.
 *
 * What cannot stay is the error text. An http.Client.Do failure arrives as a
 * *url.Error, whose Error() carries the FULL URL -- including
 * client_secret=... -- and classifyCheckError interpolated err.Error() straight
 * into the message an operator sees and the logs keep. A DNS outage, a
 * timeout, a TLS failure: any of them printed the app secret.
 *
 * alerts.ClientErrorText exists for exactly this. It unwraps *url.Error and
 * keeps the cause without the URL.
 */

func TestATransportFailureDoesNotEchoTheClientSecret(t *testing.T) {
	const secret = "notArealFacebookAppSecret.notreal"

	// A CLOSED PORT. http.Client.Do then fails with connection refused and
	// returns a *url.Error naming the request URL -- which is where the secret
	// is. Simpler and more deterministic than hijacking a live connection, and
	// it needs no t.Skip (this repo counts those).
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := srv.URL
	srv.Close()

	f := NewFacebook(WithBaseURL(base))
	err := f.CheckCredentials(context.Background(), "clientID12345", secret)
	if err == nil {
		t.Fatal("fixture: the broken connection produced no error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("the app secret is in the error an operator sees and the logs keep:\n  %s", err)
	}
	// The cause must survive — a sanitised error that says nothing is its own bug.
	if len(err.Error()) < 10 {
		t.Errorf("the error was scrubbed down to nothing useful: %q", err)
	}
}
