package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// `polyemesis mcp` is a stdio-to-HTTP relay for the MCP server (#12).
//
// Some MCP clients -- Claude Desktop's local config among them -- start a
// server as a subprocess and speak JSON-RPC on its stdin/stdout. This command
// is that subprocess: every message it reads on stdin is posted to the
// polyemesis server's /mcp endpoint with the API token, and every answer is
// written back on stdout. It holds no tools of its own and makes no decision
// about what a token may do; the server does all of that, so the relay cannot
// be a weaker path to anything.
//
// The relay is built from the official SDK's two transports (stdio lines on
// one side, streamable HTTP on the other) with messages pumped between them
// unchanged, so the protocol-version and standard headers each request needs
// are set by the same code the SDK's own client uses.
//
// THE TOKEN NEVER GOES ON THE COMMAND LINE. argv is world-readable through ps
// and lands in shell history and in the client's config file, which people
// paste into bug reports. It is read from POLYEMESIS_TOKEN or from a file
// (-token-file). A URL with http:// is refused unless the host is loopback:
// the token is a bearer credential and would cross the network in clear.
//
// stdout carries JSON-RPC and nothing else; every diagnostic goes to stderr.

const mcpProxyUsage = `usage: polyemesis mcp -url https://host[:port] [-ca ca.pem] [-token-file path]

Relays MCP over stdio to a polyemesis server's /mcp endpoint.
The API token is read from $POLYEMESIS_TOKEN, or from -token-file.
`

// runMCPProxy is the subcommand. getenv is os.Getenv outside tests.
func runMCPProxy(ctx context.Context, args []string, stdin io.ReadCloser, stdout io.WriteCloser, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, mcpProxyUsage); fs.PrintDefaults() }
	rawURL := fs.String("url", getenv("POLYEMESIS_URL"), "server base URL (or $POLYEMESIS_URL)")
	tokenFile := fs.String("token-file", "", "file holding the API token (default: $POLYEMESIS_TOKEN)")
	caFile := fs.String("ca", "", "PEM CA certificate to trust, for a server using selfsigned TLS")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	endpoint, err := mcpEndpointURL(*rawURL)
	if err != nil {
		fmt.Fprintf(stderr, "polyemesis mcp: %v\n", err)
		return 2
	}
	token := strings.TrimSpace(getenv("POLYEMESIS_TOKEN"))
	if *tokenFile != "" {
		b, err := os.ReadFile(*tokenFile)
		if err != nil {
			fmt.Fprintf(stderr, "polyemesis mcp: read the token file: %v\n", err)
			return 2
		}
		token = strings.TrimSpace(string(b))
	}
	if token == "" {
		fmt.Fprintln(stderr, "polyemesis mcp: no API token: set POLYEMESIS_TOKEN or pass -token-file")
		return 2
	}
	client, err := mcpHTTPClient(endpoint, token, *caFile)
	if err != nil {
		fmt.Fprintf(stderr, "polyemesis mcp: %v\n", err)
		return 2
	}
	if err := relayMCP(ctx, endpoint, client, stdin, stdout); err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(stderr, "polyemesis mcp: %v\n", err)
		return 1
	}
	return 0
}

// mcpEndpointURL turns the server's base URL into its /mcp endpoint, refusing
// plain HTTP to anything but loopback.
func mcpEndpointURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("-url is required (or set POLYEMESIS_URL), e.g. https://polyemesis.example")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("-url %q is not an absolute URL", raw)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("-url must not carry credentials, a query or a fragment")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLoopbackHost(u.Hostname()) {
			return "", errors.New("refusing http:// to a remote host: the API token would cross the network in clear; use https://")
		}
	default:
		return "", fmt.Errorf("-url scheme %q is not http or https", u.Scheme)
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	if !strings.HasSuffix(u.Path, "/mcp") {
		u.Path += "/mcp"
	}
	return u.String(), nil
}

// mcpHTTPClient is an HTTP client that sends the token to the endpoint and
// nowhere else, and, when caFile is given, trusts that CA in addition to the
// system roots.
//
// It FOLLOWS NO REDIRECT. A bearer token attached by a RoundTripper rides on
// every request the client makes, including the one a 3xx sends it to, so a
// server (or anything in front of it) that redirected would be handed the
// token for whatever host it named. /mcp never needs a redirect; one is
// refused as an error the relay reports. The transport also checks each
// request's scheme and host against the endpoint before attaching the token,
// so the guarantee does not rest on CheckRedirect alone.
func mcpHTTPClient(endpoint, token, caFile string) (*http.Client, error) {
	ep, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	base := http.DefaultTransport.(*http.Transport).Clone()
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("read the CA file: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("%s holds no PEM certificate", caFile)
		}
		base.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &http.Client{
		Timeout:   5 * time.Minute, // go_live paces starts and can take a while
		Transport: tokenTransport{token: token, scheme: ep.Scheme, host: ep.Host, base: base},
		CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			return fmt.Errorf("the server answered with a redirect to %s; the MCP endpoint never redirects, so it was not followed", req.URL.Redacted())
		},
	}, nil
}

// tokenTransport attaches the token to requests for the endpoint's own scheme
// and host only.
type tokenTransport struct {
	token, scheme, host string
	base                http.RoundTripper
}

func (t tokenTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != t.scheme || r.URL.Host != t.host {
		return nil, fmt.Errorf("refusing to send the API token to %s://%s; it is only for %s://%s",
			r.URL.Scheme, r.URL.Host, t.scheme, t.host)
	}
	if r.URL.Scheme == "http" && !isLoopbackHost(r.URL.Hostname()) {
		return nil, errors.New("refusing to send the API token over plain http to a remote host")
	}
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(r)
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// relayMCP pumps messages between stdio and the server until stdin closes.
func relayMCP(ctx context.Context, endpoint string, client *http.Client, stdin io.ReadCloser, stdout io.WriteCloser) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	local, err := (&mcp.IOTransport{Reader: stdin, Writer: stdout}).Connect(ctx)
	if err != nil {
		return err
	}
	defer local.Close()
	remote, err := (&mcp.StreamableClientTransport{
		Endpoint: endpoint, HTTPClient: client,
		// The server is stateless and answers each POST in its body; there is
		// no standalone stream to open.
		DisableStandaloneSSE: true,
	}).Connect(ctx)
	if err != nil {
		return err
	}
	defer remote.Close()

	var wg sync.WaitGroup
	errc := make(chan error, 2)
	wg.Add(1)
	go func() { // server -> stdout
		defer wg.Done()
		for {
			msg, err := remote.Read(ctx)
			if err != nil {
				errc <- err
				cancel()
				return
			}
			if err := local.Write(ctx, msg); err != nil {
				errc <- err
				cancel()
				return
			}
		}
	}()
	// stdin -> server, on this goroutine. A request the server could not be
	// reached for is answered locally with a JSON-RPC error, so the client is
	// told rather than left waiting for an answer that will never come.
	var readErr error
	for {
		msg, err := local.Read(ctx)
		if err != nil {
			readErr = err
			break
		}
		if err := remote.Write(ctx, msg); err != nil {
			if req, ok := msg.(*jsonrpc.Request); ok && req.IsCall() {
				resp := &jsonrpc.Response{ID: req.ID, Error: &jsonrpc.Error{
					Code: jsonrpc.CodeInternalError, Message: "polyemesis: " + oneLine(err.Error()),
				}}
				if werr := local.Write(ctx, resp); werr != nil {
					readErr = werr
					break
				}
			}
		}
	}
	cancel()
	wg.Wait()
	select {
	case err := <-errc:
		if readErr == nil {
			readErr = err
		}
	default:
	}
	return readErr
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 500 {
		s = s[:500]
	}
	return s
}
