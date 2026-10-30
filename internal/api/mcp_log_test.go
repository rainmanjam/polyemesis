package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// accessLogLevel drives one request through the real mux with the access log
// captured at Debug, and returns the level of THE line about that request --
// not whether anything at all was logged. The buffer is shared with every
// goroutine the server runs, so "the log is non-empty" says nothing about this
// request in particular.
func accessLogLevel(t *testing.T, s *Server, method, path string) (status int, level string) {
	t.Helper()
	logs := &lockedBuffer{}
	prev := s.log
	s.log = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	defer func() { s.log = prev }()
	w := do(t, s.Handler(), httptest.NewRequest(method, path, nil))
	for _, line := range strings.Split(logs.String(), "\n") {
		if !strings.Contains(line, "msg=http ") ||
			!strings.Contains(line, " method="+method+" ") ||
			!strings.Contains(line, " path="+path+" ") {
			continue
		}
		_, rest, _ := strings.Cut(line, "level=")
		level, _, _ = strings.Cut(rest, " ")
		return w.Code, level
	}
	t.Fatalf("%s %s (%d) wrote no access-log line; the test is not observing the log:\n%s",
		method, path, w.Code, logs.String())
	return 0, ""
}

// GET /mcp is a 405 on purpose, and a client asks for it on every connect.
//
// The streamable HTTP transport lets a client open a GET stream for
// server-initiated messages, and the spec has the server answer 405 when it
// offers none -- which a stateless server never does. Claude Desktop, Claude
// Code and the SDK clients all try it as they connect, so the 4xx-is-WARN rule
// put a warning in the operator's journal for every assistant session, about
// nothing. The demotion is to Debug, not to silence: the line is still there
// for whoever turns the level down to look.
//
// SCOPED TO EXACTLY THAT REQUEST. A 405 anywhere else is still a client using
// the API wrongly, and an unauthenticated POST /mcp is still a 401 somebody may
// need to see; both stay at WARN, and are asserted here as the control.
//
// Mutation: drop the GET /mcp 405 case in requestLogger. Observed FAIL
// (level WARN). Mutation: widen it to every 405. Observed FAIL on the
// /api/v1/auth/login control.
func TestTheExpectedGETOnTheMCPEndpointIsLoggedAtDebugAndOther405sStayAtWarn(t *testing.T) {
	s := serverUnderTest(t, newMCPRig(t).h)

	// The real mux answers an unauthenticated GET /mcp with 405, not 401: the
	// method is refused before the token group's middleware runs. If that ever
	// changes, this test would be asserting about a request nobody sends.
	status, level := accessLogLevel(t, s, http.MethodGet, MCPPath)
	if status != http.StatusMethodNotAllowed {
		t.Fatalf("GET %s answered %d, want 405", MCPPath, status)
	}
	if level != "DEBUG" {
		t.Errorf("GET %s's expected 405 was logged at %s, want DEBUG", MCPPath, level)
	}

	for _, c := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/api/v1/auth/login", http.StatusMethodNotAllowed},
		{http.MethodDelete, MCPPath, http.StatusMethodNotAllowed},
		{http.MethodPost, MCPPath, http.StatusUnauthorized},
	} {
		status, level := accessLogLevel(t, s, c.method, c.path)
		if status != c.want {
			t.Fatalf("%s %s answered %d, want %d", c.method, c.path, status, c.want)
		}
		if level != "WARN" {
			t.Errorf("%s %s (%d) was logged at %s; only GET %s's 405 is demoted, want WARN",
				c.method, c.path, status, level, MCPPath)
		}
	}
}
