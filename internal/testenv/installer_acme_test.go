package testenv

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TWO STRUCTURAL FACTS ABOUT install.sh, PINNED BECAUSE LOSING EITHER
// UNINSTALLS A WORKING SERVER.
//
// #642: verify() probed https://127.0.0.1:PORT with -k in every TLS mode. In
// acme mode that handshake carries no SNI, and internal/tlsx's acme path sets
// only conf.GetCertificate -- no conf.Certificates fallback, unlike selfsigned
// whose leaf carries 127.0.0.1. autocert has no name to look up, so the
// connection aborts before any HTTP happens; -k skips VERIFYING a certificate
// and cannot invent one. verify() then returned 1, the caller exited before
// INSTALL_COMPLETE was set, and the EXIT trap removed the unit, the binary,
// /etc/polyemesis, /opt/polyemesis and the service account -- of a server that
// was working -- and printed that nothing was left running.
//
// Neither half is expressible in the type system and neither is covered by CI,
// which runs `install.sh --check` and never installs with acme. So this reads
// the script. Warning rung: it announces a regression rather than preventing
// one, which is what a shell script admits of.

func installerSource(t *testing.T) string {
	t.Helper()
	root := repoRootFromTest(t)
	b, err := os.ReadFile(filepath.Join(root, "scripts", "install.sh"))
	if err != nil {
		t.Fatalf("reading scripts/install.sh: %v", err)
	}
	return string(b)
}

func TestVerifyDoesNotProbeLoopbackTLSInACMEMode(t *testing.T) {
	src := installerSource(t)

	body := functionBody(t, src, "verify")
	// The acme branch must come before the https probe is built, and must
	// return rather than fall through.
	acme := strings.Index(body, `"$TLS_MODE" = acme`)
	if acme < 0 {
		t.Fatal("verify() no longer branches on acme.\n" +
			"In acme mode there is no certificate for 127.0.0.1 and no SNI on that " +
			"connection, so an https probe there fails on a healthy server -- and a " +
			"failing verify() makes the EXIT trap uninstall it. See #642.")
	}
	probe := strings.Index(body, "https://127.0.0.1")
	if probe >= 0 && probe < acme {
		t.Error("verify() builds the loopback https probe before its acme branch; " +
			"in acme mode that probe cannot succeed on a healthy server")
	}
	if !strings.Contains(body, "verify_acme_redirect") {
		t.Error("verify() does not delegate acme mode to a check that can pass on loopback")
	}
}

func TestTheFailureTrapWillNotRemoveARunningService(t *testing.T) {
	src := installerSource(t)
	body := functionBody(t, src, "cleanup_on_failure")

	guard := strings.Index(body, "systemctl is-active")
	if guard < 0 {
		t.Fatal("cleanup_on_failure no longer checks whether the service is running.\n" +
			"Every removal below that point deletes what the install created, which is " +
			"right when the install failed and catastrophic when only a CHECK failed: " +
			"#642 uninstalled a working server because verify() could not complete a " +
			"TLS handshake on loopback.")
	}

	// The guard has to sit above the destructive steps, or it guards nothing.
	for _, destructive := range []string{"systemctl disable", "userdel", "rm -rf"} {
		if at := strings.Index(body, destructive); at >= 0 && at < guard {
			t.Errorf("cleanup_on_failure runs %q before the is-active guard, so a "+
				"running server is torn down before anything asks whether it is running",
				destructive)
		}
	}
}

// functionBody returns the text of a shell function, from its `name() {` to
// the closing brace at column zero.
func functionBody(t *testing.T, src, name string) string {
	t.Helper()
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(name) + `\(\) \{$`)
	loc := re.FindStringIndex(src)
	if loc == nil {
		t.Fatalf("could not find shell function %s() in install.sh.\n"+
			"If it was renamed, update this test in the same commit -- it cannot "+
			"check a function it can no longer find, and would pass by finding nothing.", name)
	}
	rest := src[loc[1]:]
	end := strings.Index(rest, "\n}\n")
	if end < 0 {
		t.Fatalf("could not find the end of %s()", name)
	}
	return stripShellComments(rest[:end])
}

// stripShellComments removes whole-line and trailing comments.
//
// Both assertions here are about ORDER, and both of these functions carry long
// comments that quote the very code being searched for -- the note on verify()
// names https://127.0.0.1 to explain why it must not be probed, and the note
// on the trap names the destructive commands to explain what it is guarding.
// Searching the raw text found those sentences and reported the bug the
// comments describe as still present. A guard that reads prose reports the
// explanation as the defect.
func stripShellComments(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			// Not inside a quoted string: these scripts have no # in code
			// positions that matters for the order questions asked here.
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}
