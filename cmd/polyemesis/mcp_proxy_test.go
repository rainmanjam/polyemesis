package main

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The relay is tested end to end with the official SDK on both sides: an SDK
// client speaking newline JSON on a pipe (what Claude Desktop does to a
// subprocess), the relay in the middle, and a stateless SDK server behind
// HTTP that refuses anything without the expected bearer token -- the shape of
// polyemesis's own /mcp.

const relayTestToken = "pk_test_relay_token_0123456789"

func relayTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "fake", Version: "1"}, nil)
	srv.AddTool(&mcp.Tool{Name: "echo", InputSchema: map[string]any{"type": "object"}},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "auth=" + req.Extra.Header.Get("Authorization")}}}, nil
		})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+relayTestToken {
			http.Error(w, `{"error":"not signed in"}`, http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts
}

// relayClient starts the relay on a pipe pair and connects an SDK client to
// the other ends.
func relayClient(t *testing.T, baseURL string, env map[string]string) (*mcp.ClientSession, *bytes.Buffer, func() int) {
	t.Helper()
	inR, inW := io.Pipe()   // client -> relay stdin
	outR, outW := io.Pipe() // relay stdout -> client
	var stderr bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	var code int
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		code = runMCPProxy(ctx, []string{"-url", baseURL}, inR, outW, &stderr, func(k string) string { return env[k] })
		_ = outW.Close()
	}()
	cctx, ccancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(ccancel)
	client := mcp.NewClient(&mcp.Implementation{Name: "desktop", Version: "1"}, nil)
	cs, err := client.Connect(cctx, &mcp.IOTransport{Reader: outR, Writer: inW}, nil)
	finish := func() int {
		_ = inW.Close()
		cancel()
		wg.Wait()
		return code
	}
	if err != nil {
		finish()
		t.Fatalf("connect through the relay: %v (stderr: %s)", err, stderr.String())
	}
	t.Cleanup(func() { _ = cs.Close(); finish() })
	return cs, &stderr, finish
}

func TestTheStdioRelayCarriesToolsAndSendsTheTokenFromTheEnvironment(t *testing.T) {
	ts := relayTestServer(t)
	cs, stderr, _ := relayClient(t, ts.URL, map[string]string{"POLYEMESIS_TOKEN": relayTestToken})
	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil || len(tools.Tools) != 1 || tools.Tools[0].Name != "echo" {
		t.Fatalf("tools/list through the relay: %v %+v (stderr %s)", err, tools, stderr)
	}
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("tools/call through the relay: %v", err)
	}
	if got := res.Content[0].(*mcp.TextContent).Text; got != "auth=Bearer "+relayTestToken {
		t.Fatalf("the server saw %q", got)
	}
}

// A wrong token is the server's 401, and the client hears it as an error
// rather than waiting forever for an answer.
func TestTheStdioRelayAnswersARefusedRequestRatherThanHanging(t *testing.T) {
	ts := relayTestServer(t)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	var stderr bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() {
		done <- runMCPProxy(ctx, []string{"-url", ts.URL}, inR, outW, &stderr,
			func(k string) string { return map[string]string{"POLYEMESIS_TOKEN": "wrong-token-000000"}[k] })
		_ = outW.Close()
	}()
	go func() {
		_, _ = io.WriteString(inW, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"x","version":"1"}}}`+"\n")
	}()
	line := make([]byte, 4096)
	type readResult struct {
		n   int
		err error
	}
	rc := make(chan readResult, 1)
	go func() { n, err := outR.Read(line); rc <- readResult{n, err} }()
	select {
	case r := <-rc:
		if r.err != nil || !strings.Contains(string(line[:r.n]), `"error"`) || !strings.Contains(string(line[:r.n]), `"id":1`) {
			t.Fatalf("the refused initialize was answered with %q (%v)", line[:r.n], r.err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the relay never answered a request the server refused")
	}
	_ = inW.Close()
	cancel()
	<-done
}

func TestTheStdioRelayRefusesToStartWithoutATokenOrWithAPlainHTTPRemote(t *testing.T) {
	cases := []struct {
		name string
		args []string
		env  map[string]string
		want string
	}{
		{"no token", []string{"-url", "https://pe.example"}, nil, "no API token"},
		{"no url", nil, map[string]string{"POLYEMESIS_TOKEN": "x"}, "-url is required"},
		{"plain http remote", []string{"-url", "http://pe.example"}, map[string]string{"POLYEMESIS_TOKEN": "x"}, "refusing http://"},
		{"credentials in url", []string{"-url", "https://u:p@pe.example"}, map[string]string{"POLYEMESIS_TOKEN": "x"}, "must not carry credentials"},
		{"missing ca", []string{"-url", "https://pe.example", "-ca", "/nonexistent/ca.pem"}, map[string]string{"POLYEMESIS_TOKEN": "x"}, "read the CA file"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var stderr bytes.Buffer
			var stdout nopWriteCloser
			code := runMCPProxy(context.Background(), c.args, io.NopCloser(strings.NewReader("")), &stdout, &stderr,
				func(k string) string { return c.env[k] })
			if code == 0 || !strings.Contains(stderr.String(), c.want) {
				t.Fatalf("exit %d, stderr %q, want it to say %q", code, stderr.String(), c.want)
			}
			if stdout.Len() != 0 {
				t.Fatalf("a refusal wrote to stdout, which belongs to JSON-RPC: %q", stdout.String())
			}
		})
	}
}

func TestTheStdioRelayReadsTheTokenFromAFileAndTrimsIt(t *testing.T) {
	ts := relayTestServer(t)
	f := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(f, []byte(relayTestToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		runMCPProxy(ctx, []string{"-url", ts.URL, "-token-file", f}, inR, outW, io.Discard, func(string) string { return "" })
		_ = outW.Close()
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "desktop", Version: "1"}, nil)
	cctx, ccancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer ccancel()
	cs, err := client.Connect(cctx, &mcp.IOTransport{Reader: outR, Writer: inW}, nil)
	if err != nil {
		t.Fatalf("connect with a token file: %v", err)
	}
	defer cs.Close()
	if _, err := cs.ListTools(context.Background(), nil); err != nil {
		t.Fatalf("tools/list with a token file: %v", err)
	}
	_ = inW.Close()
}

func TestMCPEndpointURLAppendsTheMCPPathOnce(t *testing.T) {
	for in, want := range map[string]string{
		"https://pe.example":          "https://pe.example/mcp",
		"https://pe.example/":         "https://pe.example/mcp",
		"https://pe.example/mcp":      "https://pe.example/mcp",
		"http://127.0.0.1:8080":       "http://127.0.0.1:8080/mcp",
		"http://localhost:8080/mcp/":  "http://localhost:8080/mcp",
		"https://pe.example:8443/sub": "https://pe.example:8443/sub/mcp",
	} {
		got, err := mcpEndpointURL(in)
		if err != nil || got != want {
			t.Errorf("mcpEndpointURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

// A redirect is never followed, and the token never reaches the host it
// names. Server A answers /mcp with a 307 to server B; B records every
// Authorization header it is sent. The relay must answer the client's request
// with an error and B must have seen no token -- the shape of a token leaked to
// whatever a compromised proxy redirects to.
//
// Mutation: drop CheckRedirect and the host check in tokenTransport. Observed
// FAIL (B received "Bearer pk_test_relay_token_...").
func TestTheStdioRelayFollowsNoRedirectAndNeverSendsTheTokenElsewhere(t *testing.T) {
	var mu sync.Mutex
	var seenByB []string
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenByB = append(seenByB, r.Header.Get("Authorization"))
		mu.Unlock()
		http.Error(w, "no", http.StatusUnauthorized)
	}))
	defer b.Close()
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, b.URL+"/mcp", http.StatusTemporaryRedirect)
	}))
	defer a.Close()

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	var stderr bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		runMCPProxy(ctx, []string{"-url", a.URL}, inR, outW, &stderr,
			func(k string) string { return map[string]string{"POLYEMESIS_TOKEN": relayTestToken}[k] })
		_ = outW.Close()
		close(done)
	}()
	go func() {
		_, _ = io.WriteString(inW, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"x","version":"1"}}}`+"\n")
	}()
	line := make([]byte, 4096)
	n, _ := outR.Read(line)
	if !strings.Contains(string(line[:n]), `"error"`) {
		t.Fatalf("a redirected request was answered with %q, want a JSON-RPC error", line[:n])
	}
	_ = inW.Close()
	cancel()
	<-done
	mu.Lock()
	defer mu.Unlock()
	for _, h := range seenByB {
		if h != "" {
			t.Fatalf("the redirect target received an Authorization header: %q", h)
		}
	}
}

func TestTheTokenTransportRefusesAnyOtherHostAndPlainHTTPToARemoteHost(t *testing.T) {
	var got []string
	base := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		got = append(got, r.URL.Host+"="+r.Header.Get("Authorization"))
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}, nil
	})
	tt := tokenTransport{token: "tok", scheme: "https", host: "pe.example", base: base}
	for _, u := range []string{"https://evil.example/mcp", "http://pe.example/mcp", "https://pe.example:444/mcp"} {
		req, _ := http.NewRequest(http.MethodPost, u, nil)
		if _, err := tt.RoundTrip(req); err == nil {
			t.Errorf("the token transport sent a request to %s", u)
		}
	}
	plain := tokenTransport{token: "tok", scheme: "http", host: "pe.example", base: base}
	req, _ := http.NewRequest(http.MethodPost, "http://pe.example/mcp", nil)
	if _, err := plain.RoundTrip(req); err == nil {
		t.Error("the token transport sent the token over plain http to a remote host")
	}
	req, _ = http.NewRequest(http.MethodPost, "https://pe.example/mcp", nil)
	if _, err := tt.RoundTrip(req); err != nil {
		t.Fatalf("the endpoint itself was refused: %v", err)
	}
	if len(got) != 1 || got[0] != "pe.example=Bearer tok" {
		t.Fatalf("requests that reached the network: %v", got)
	}
}

// An older client: Claude Desktop may negotiate 2025-11-25, whose messages
// carry no protocol version in _meta, so a raw relay never learns it from the
// message. Driven as raw newline JSON (not the SDK client, which always speaks
// the newest version) through the whole lifecycle, against the real stateless
// SDK handler polyemesis serves.
func TestTheStdioRelayWorksForAClientSpeaking2025_11_25(t *testing.T) {
	ts := relayTestServer(t)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	var stderr bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		runMCPProxy(ctx, []string{"-url", ts.URL}, inR, outW, &stderr,
			func(k string) string { return map[string]string{"POLYEMESIS_TOKEN": relayTestToken}[k] })
		_ = outW.Close()
		close(done)
	}()
	lines := bufio.NewScanner(outR)
	lines.Buffer(make([]byte, 1<<20), 1<<20)
	read := func() string {
		got := make(chan string, 1)
		go func() {
			if lines.Scan() {
				got <- lines.Text()
			} else {
				got <- ""
			}
		}()
		select {
		case l := <-got:
			return l
		case <-time.After(15 * time.Second):
			t.Fatalf("the relay answered nothing (stderr %s)", stderr.String())
			return ""
		}
	}
	send := func(msg string) {
		if _, err := io.WriteString(inW, msg+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"desktop","version":"1"}}}`)
	if l := read(); !strings.Contains(l, `"protocolVersion":"2025-11-25"`) {
		t.Fatalf("initialize answered %s", l)
	}
	send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if l := read(); !strings.Contains(l, `"id":2`) || !strings.Contains(l, `"echo"`) {
		t.Fatalf("tools/list answered %s", l)
	}
	send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{}}}`)
	if l := read(); !strings.Contains(l, `"id":3`) || !strings.Contains(l, "auth=Bearer "+relayTestToken) {
		t.Fatalf("tools/call answered %s", l)
	}
	_ = inW.Close()
	cancel()
	<-done
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type nopWriteCloser struct{ bytes.Buffer }

func (nopWriteCloser) Close() error { return nil }
