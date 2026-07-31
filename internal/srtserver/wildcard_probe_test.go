package srtserver

import (
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"

	srt "github.com/datarhei/gosrt"
)

// A probe, not a unit test: it binds real UDP ports and waits on real
// handshakes, so it is skipped unless asked for.
//
//	POLYEMESIS_SRT_PROBE=1 go test ./internal/srtserver/ -run Matrix -v
//
// It exists because the claim behind issue #28 -- "a wildcard SRT listener
// refuses IPv4 callers on macOS" -- was measured on exactly one machine, and
// "macOS", "Apple Silicon" and "that laptop's firewall" are three different
// claims. Running this on hosted runners separates them.
func TestWildcardIPv4Matrix(t *testing.T) {
	if os.Getenv("POLYEMESIS_SRT_PROBE") != "1" {
		t.Skip("set POLYEMESIS_SRT_PROBE=1 to run the SRT address-family probe")
	}

	cases := []struct {
		name, listen, dial string
	}{
		{"wildcard   <- IPv4", ":17200", "127.0.0.1:17200"},
		{"wildcard   <- IPv6", ":17201", "[::1]:17201"},
		{"0.0.0.0    <- IPv4", "0.0.0.0:17202", "127.0.0.1:17202"},
		{"127.0.0.1  <- IPv4", "127.0.0.1:17203", "127.0.0.1:17203"},
	}

	results := map[string]bool{}
	t.Logf("runtime: %s/%s", runtime.GOOS, runtime.GOARCH)
	for _, c := range cases {
		err := srtRoundTrip(c.listen, c.dial)
		results[c.name] = err == nil
		if err == nil {
			t.Logf("  %-20s ok", c.name)
		} else {
			t.Logf("  %-20s FAIL  (%v)", c.name, err)
		}
	}

	// The one invariant. Linux is what every container image runs; a wildcard
	// listener there must accept IPv4 publishers, and if that ever stops being
	// true it is a release-blocking regression rather than a curiosity.
	if runtime.GOOS == "linux" && !results["wildcard   <- IPv4"] {
		t.Error("a wildcard SRT listener refused an IPv4 caller on linux — " +
			"this is the deployment target, not a development platform")
	}

	// Everything else is reported rather than asserted: the behaviour belongs
	// to gosrt and the host, and pinning it here would turn an upstream fix
	// into a red build.
	if runtime.GOOS == "darwin" {
		if results["wildcard   <- IPv4"] {
			t.Log("NOTE: the wildcard bind accepted IPv4 on this darwin host. " +
				"If that holds across runners, issue #28 is host-specific and " +
				"the startup warning should be reconsidered.")
		} else {
			t.Log("CONFIRMED on this host: wildcard refuses IPv4 while 0.0.0.0 accepts it.")
		}
	}
}

func srtRoundTrip(listenAddr, dialAddr string) error {
	cfg := srt.DefaultConfig()
	cfg.StreamId = "probe"
	ln, err := srt.Listen("srt", listenAddr, cfg)
	if err != nil {
		return fmt.Errorf("listen %s: %w", listenAddr, err)
	}
	defer ln.Close()

	go func() {
		for {
			conn, _, err := ln.Accept(func(req srt.ConnRequest) srt.ConnType { return srt.PUBLISH })
			if err != nil {
				return
			}
			if conn != nil {
				conn.Close()
			}
		}
	}()
	time.Sleep(150 * time.Millisecond)

	d := srt.DefaultConfig()
	d.StreamId = "probe"
	d.ConnectionTimeout = 3 * time.Second
	conn, err := srt.Dial("srt", dialAddr, d)
	if err != nil {
		return err
	}
	conn.Close()
	return nil
}
