package chat

// PKCS#1 v1.5 HERE IS KICK'S CHOICE, NOT OURS, AND IT IS VERIFICATION ONLY.
//
// Sonar reports go:S5542 as CRITICAL against kick_verify.go: "use a secure mode
// and padding scheme for this encryption algorithm". The rule is right about
// PKCS#1 v1.5 in general and wrong about this call, for two reasons that are
// worth pinning rather than asserting in a comment:
//
//   1. It VERIFIES. rsa.VerifyPKCS1v15 checks a signature Kick produced. The
//      Bleichenbacher attacks that make v1.5 dangerous are against DECRYPTION
//      oracles; there is no decryption here and no private key in this process.
//
//   2. The padding is not a choice we get to make. Kick signs its webhooks this
//      way. Switching to PSS would reject every genuine delivery -- the failure
//      would be total and silent-looking: webhooks simply stop arriving.
//
// The hazard worth guarding is that this exemption spreads. A later
// rsa.SignPKCS1v15 or rsa.DecryptPKCS1v15 anywhere in the tree would be a real
// finding wearing the same rule id, and the scoped Sonar ignore that silences
// this file would be read as covering it. So: v1.5 may appear in this one file,
// and only as verification.
//
// WARNING rung. Control would be a wrapper package exporting only Verify, with
// crypto/rsa unimportable elsewhere -- Go has no import restriction that
// expresses that within a module, which is the honest reason this is a test and
// not a compiler error.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPKCS1v15IsConfinedToVerifyingKicksSignature(t *testing.T) {
	root := "../.."
	var uses []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			if info != nil && info.IsDir() {
				switch info.Name() {
				case ".git", ".claude", "node_modules", "vendor", "dist", ".gitnexus":
					return filepath.SkipDir
				}
			}
			return err
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(b), "\n") {
			// A comment that names the function is discussing it, not calling
			// it -- and one of them is a note explaining why the acceptance
			// driver deliberately does NOT sign.
			if t := strings.TrimSpace(line); !strings.Contains(t, "PKCS1v15") || strings.HasPrefix(t, "//") {
				continue
			}
			rel := strings.TrimPrefix(filepath.ToSlash(path), "../../")
			uses = append(uses, rel+": "+strings.TrimSpace(line))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(uses) == 0 {
		t.Fatal("no PKCS1v15 use found anywhere, including the Kick verifier this " +
			"guard exists for. Either the walk is broken or the verifier moved; " +
			"a guard that finds nothing is not evidence of anything.")
	}
	for _, u := range uses {
		file := strings.SplitN(u, ":", 2)[0]
		if file != "internal/chat/kick_verify.go" {
			t.Errorf("PKCS#1 v1.5 outside the Kick verifier:\n  %s\n\n"+
				"The scoped Sonar ignore for go:S5542 covers kick_verify.go only, and it "+
				"is justified by that call being signature VERIFICATION of a third "+
				"party's fixed format. Anywhere else this is a real finding.", u)
		}
		if strings.Contains(u, "SignPKCS1v15") || strings.Contains(u, "DecryptPKCS1v15") {
			t.Errorf("PKCS#1 v1.5 used to sign or decrypt, not verify:\n  %s\n\n"+
				"The exemption rests entirely on there being no decryption oracle and no "+
				"private key here.", u)
		}
	}
}
