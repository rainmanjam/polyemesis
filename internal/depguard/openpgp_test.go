package depguard

import (
	"os/exec"
	"strings"
	"testing"
)

// The package GO-2026-5932 is about. Unmaintained, unsafe by design, and with
// no upstream fix coming ("Fixed in: N/A"), so there is nothing to wait for.
const forbidden = "golang.org/x/crypto/openpgp"

/*
TestOpenPGPStaysOutOfTheBuild pins the property the advisory currently turns on.

govulncheck already runs on every push and pull request and would go red the
day this package became reachable -- that part is covered, and this test does
not duplicate it. What it adds is the REASON. govulncheck reports a CVE
identifier against a module; this reports the sentence, at the moment the
import is added, to the person adding it, in a local `go test` rather than in
CI five minutes later.

The distinction matters here more than usual because "not called" is a property
of today's code and not a guarantee. x/crypto is a dependency this repository
genuinely needs -- internal/tlsx builds on x/crypto/acme for certificate
issuance, and the secrets box uses nacl/secretbox -- so the module edge is not
removable and the advisory cannot be resolved by dropping it. Only the package
boundary can be held, and only deliberately.

Asked of `go list` rather than by grepping for the string: an import added
through a dependency would not appear in this repository's own source, and the
whole point is to catch the edge however it arrives.
*/
func TestOpenPGPStaysOutOfTheBuild(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "../../...").CombinedOutput()
	if err != nil {
		// A toolchain that cannot answer is not a pass. Reporting "no openpgp
		// found" because `go list` failed would be the same defect this
		// repository has already named once: an absence reading as an answer.
		t.Fatalf("go list -deps failed, so this guard proved nothing: %v\n%s", err, out)
	}

	for _, line := range strings.Split(string(out), "\n") {
		pkg := strings.TrimSpace(line)
		if pkg == forbidden || strings.HasPrefix(pkg, forbidden+"/") {
			t.Fatalf(
				"%s is in the build graph.\n\n"+
					"It is unmaintained, unsafe by design, and has no upstream fix "+
					"(GO-2026-5932, Fixed in: N/A), so importing it is a decision that "+
					"cannot be walked back by upgrading. If OpenPGP is genuinely needed, "+
					"use a maintained implementation and delete this guard deliberately "+
					"rather than working around it.", pkg)
		}
	}
}

/*
THE CONTROL CASE. A guard that finds nothing passes whether or not it is
looking in the right place -- a typo in the module path, a `go list` invocation
scoped to an empty directory, or output this test failed to read would all
produce a green tick that means nothing.

So it also asserts that a package x/crypto DOES ship is present. That fails if
the listing is empty or scoped wrongly, which is every way the check above
could be silently vacuous.
*/
func TestTheGuardIsActuallyLookingAtTheBuildGraph(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "../../...").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps failed: %v\n%s", err, out)
	}
	// internal/tlsx builds on this for ACME certificate issuance; if it is
	// absent, the listing above is not describing this repository.
	const known = "golang.org/x/crypto/acme"
	if !strings.Contains(string(out), known) {
		t.Fatalf("%s is not in the listing, so the check above was searching "+
			"something other than this module's build graph", known)
	}
}
