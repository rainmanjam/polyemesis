package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/tlsx"
)

// An install upgraded from a release that minted an unconstrained CA gets a
// new CA on its first start (see tlsx.caReplacementReason), and so does one
// whose tls.hostname changed. Every client that trusted the old CA breaks at
// that moment, so the start must say so, say why, and name the file to trust
// instead.
func TestAReplacedLocalCAIsAnnouncedAtStartup(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))

	// First start: a fresh CA, nothing anyone trusted was replaced.
	first, err := tlsx.New(tlsx.Options{Mode: tlsx.ModeSelfSigned, Hostname: "old.local", DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	warnIfCAReplaced(log, first, "/data/tls/ca.crt")
	if strings.Contains(buf.String(), "local CA was replaced") {
		t.Errorf("a first start announced a replaced CA:\n%s", buf.String())
	}

	// A hostname change replaces the CA.
	second, err := tlsx.New(tlsx.Options{Mode: tlsx.ModeSelfSigned, Hostname: "new.local", DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	warnIfCAReplaced(log, second, "/data/tls/ca.crt")
	out := buf.String()
	for _, want := range []string{"level=WARN", "local CA was replaced", "Remove the old", "tls.hostname changed", "/data/tls/ca.crt", second.CAFingerprint()} {
		if !strings.Contains(out, want) {
			t.Errorf("the startup warning does not contain %q:\n%s", want, out)
		}
	}
}
