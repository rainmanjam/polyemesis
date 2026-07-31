package srtserver

import (
	"bytes"
	"log/slog"
	"runtime"
	"strings"
	"testing"
)

// The macOS wildcard warning.
//
// Issue #28 was filed as "the SRT listener may bind IPv6-only", on the evidence
// that lsof showed only an IPv6 socket. That evidence was misread: a Go
// dual-stack socket looks exactly like that, and a plain net.ListenPacket on
// the same address on the same machine receives IPv4 datagrams fine.
//
// The real behaviour, measured against gosrt v0.11.0: a wildcard listener on
// macOS never completes the handshake for an IPv4 caller, while the same code
// on Linux does. Since handleConnect is never reached, none of its typed
// refusals fire and both ends stay silent -- which is the part worth fixing
// even though the cause is upstream and platform-specific.
func TestWildcardWarningIsDarwinOnly(t *testing.T) {
	var buf bytes.Buffer
	s := New(slog.New(slog.NewTextHandler(&buf, nil)), ":6000", nil)
	s.warnIfWildcardOnDarwin()

	got := buf.String()
	warned := strings.Contains(got, "accepts IPv6 publishers only")

	if runtime.GOOS == "darwin" {
		if !warned {
			t.Fatal("no warning on darwin, where a wildcard bind refuses IPv4 publishers")
		}
		if !strings.Contains(got, "0.0.0.0:6000") {
			t.Errorf("the warning does not name the address to use instead: %s", got)
		}
		return
	}
	if warned {
		t.Fatalf("warned on %s, where the wildcard bind works: %s", runtime.GOOS, got)
	}
}

// An explicit host is not the failing shape and must never warn, on any
// platform -- including the one where the bug exists.
func TestExplicitHostNeverWarns(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:6000", "127.0.0.1:6000", "[::]:6000"} {
		var buf bytes.Buffer
		s := New(slog.New(slog.NewTextHandler(&buf, nil)), addr, nil)
		s.warnIfWildcardOnDarwin()
		if strings.Contains(buf.String(), "accepts IPv6 publishers only") {
			t.Errorf("warned about %s, which is not the failing shape", addr)
		}
	}
}
