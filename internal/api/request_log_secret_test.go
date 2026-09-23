package api

import (
	"bytes"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/config"
)

// THE PATH IS THE CREDENTIAL, AND THE REQUEST LOG PRINTED IT. Kick's webhook
// proves itself by knowing /api/v1/chat/kick/{secret}, and requestLogger logs
// every 4xx at WARN and every 5xx at ERROR -- with the path. So a GET on the
// route, a body that is not JSON, a bad signature: each wrote the secret into
// journald, on exactly the requests that were not Kick's. SECURITY.md says the
// secret is never logged.
func TestARefusedKickWebhookDoesNotLogItsSecret(t *testing.T) {
	s, h, _ := testServer(t, config.Config{})
	s.chat = chat.New()
	t.Cleanup(s.chat.Close)
	attachKick(t, s, "12345")
	secret := s.kickCallbackSecret()
	var buf bytes.Buffer
	s.log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// A method Kick never uses, and an unsigned POST: two of the refusals the
	// adapter's receiver answers with a 4xx.
	for _, method := range []string{http.MethodPut, http.MethodPost} {
		w := do(t, h, jsonRequest(t, method, "/api/v1/chat/kick/"+secret, map[string]string{"event": "x"}))
		if w.Code < 400 {
			t.Fatalf("%s on the Kick webhook answered %d; this test needs a refusal to be logged", method, w.Code)
		}
	}

	var lines []string
	for _, l := range strings.Split(buf.String(), "\n") {
		if strings.Contains(l, "msg=http") {
			lines = append(lines, l)
		}
	}
	if len(lines) != 2 {
		t.Fatalf("want 2 request log lines, got %d:\n%s", len(lines), buf.String())
	}
	for _, l := range lines {
		if strings.Contains(l, secret) {
			t.Errorf("the request log carries the Kick webhook secret:\n%s", l)
		}
		if !strings.Contains(l, "path=/api/v1/chat/kick/{secret}") {
			t.Errorf("the request log line does not name the route it matched:\n%s", l)
		}
	}
}

// A path that matched nothing keeps its raw path in the log: which unknown URL
// is being asked for is what a 404 line is for.
func TestAnUnroutedPathIsStillLoggedAsItArrived(t *testing.T) {
	s, h, _ := testServer(t, config.Config{})
	var buf bytes.Buffer
	s.log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	do(t, h, jsonRequest(t, http.MethodGet, "/api/v1/no-such-route/abc", nil))
	if !strings.Contains(buf.String(), "path=/api/v1/no-such-route/abc") {
		t.Fatalf("an unrouted request was not logged with its path:\n%s", buf.String())
	}
}
