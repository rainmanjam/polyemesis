package main

import (
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

type nopWriteCloser struct{ bytes.Buffer }

func (nopWriteCloser) Close() error { return nil }
