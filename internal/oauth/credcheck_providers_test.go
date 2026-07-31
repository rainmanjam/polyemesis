package oauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// checkFixture swaps the shared httpClient for one pointed at a fake platform,
// and restores it afterwards. The package uses one httpClient (oauth.go:117),
// so this is the seam available without restructuring.
func checkFixture(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	prev := httpClient
	httpClient = srv.Client()
	t.Cleanup(func() {
		httpClient = prev
		srv.Close()
	})
	return srv
}

func TestTwitchCheckCredentials(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		body        string
		wantErr     bool
		unreachable bool
	}{
		{name: "accepted", status: 200, body: `{"access_token":"a","expires_in":3600}`},
		{name: "bad secret", status: 403, body: `{"status":403,"message":"invalid client secret"}`, wantErr: true},
		{name: "bad client id", status: 400, body: `{"status":400,"message":"invalid client"}`, wantErr: true},
		{name: "platform broken", status: 500, body: `oops`, wantErr: true, unreachable: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := checkFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if got := r.FormValue("grant_type"); got != "client_credentials" {
					t.Errorf("grant_type = %q, want client_credentials", got)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			tw := &Twitch{tokenURL: srv.URL}

			err := tw.CheckCredentials(context.Background(), "id", "secret")
			if tc.wantErr && err == nil {
				t.Fatal("accepted, want an error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("rejected a valid pair: %v", err)
			}
			if got := errors.Is(err, ErrCheckUnreachable); got != tc.unreachable {
				t.Fatalf("errors.Is(err, ErrCheckUnreachable) = %v, want %v (err = %v)",
					got, tc.unreachable, err)
			}
		})
	}
}

func TestCheckCredentialsNeverEchoesTheSecret(t *testing.T) {
	// The detail string is rendered in the UI. It must never carry the input.
	const secret = "s3cr3t-do-not-print-me"
	srv := checkFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"message":"invalid client secret"}`))
	})
	tw := &Twitch{tokenURL: srv.URL}

	err := tw.CheckCredentials(context.Background(), "id", secret)
	if err == nil {
		t.Fatal("expected rejection")
	}
	if contains(err.Error(), secret) {
		t.Fatalf("the error text contains the secret: %q", err.Error())
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		(haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}

func TestKickCheckCredentials(t *testing.T) {
	t.Run("accepted", func(t *testing.T) {
		srv := checkFixture(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/oauth/token" {
				t.Errorf("path = %q, want /oauth/token", r.URL.Path)
			}
			if got := r.FormValue("grant_type"); got != "client_credentials" {
				t.Errorf("grant_type = %q", got)
			}
			_, _ = w.Write([]byte(`{"access_token":"a","expires_in":3600}`))
		})
		k := &Kick{idBase: srv.URL}
		if err := k.CheckCredentials(context.Background(), "id", "secret"); err != nil {
			t.Fatalf("rejected a valid pair: %v", err)
		}
	})

	t.Run("rejected", func(t *testing.T) {
		srv := checkFixture(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
		})
		k := &Kick{idBase: srv.URL}
		err := k.CheckCredentials(context.Background(), "id", "wrong")
		if err == nil {
			t.Fatal("accepted, want rejection")
		}
		if errors.Is(err, ErrCheckUnreachable) {
			t.Fatal("a 401 is a refusal, not an unreachable platform")
		}
	})
}

func TestFacebookCheckCredentialsUsesGET(t *testing.T) {
	// The shape matters: Facebook answers a POSTed form with 400 whatever the
	// credentials are, so reusing postForm would report every correct pair as
	// rejected.
	var method, path string
	srv := checkFixture(t, func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		_, _ = w.Write([]byte(`{"access_token":"app-token"}`))
	})
	f := &Facebook{graphBase: srv.URL}

	if err := f.CheckCredentials(context.Background(), "id", "secret"); err != nil {
		t.Fatalf("rejected a valid pair: %v", err)
	}
	if method != http.MethodGet {
		t.Errorf("method = %s, want GET", method)
	}
	if path != "/oauth/access_token" {
		t.Errorf("path = %q, want /oauth/access_token", path)
	}
}
