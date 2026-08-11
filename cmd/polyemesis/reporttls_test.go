package main

import (
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/tlsx"
)

// TestReportTLSTellsTheOperatorWhatTheBrowserWillSee covers the two arms of the
// startup TLS report that a reader cannot get anywhere else.
//
// Both are DECISIONS, not decorations, which is the bar for putting a banner
// line under test at all:
//
//  1. THE CA FINGERPRINT IS AN OPERATOR CONTRACT, and it is asserted nowhere
//     else in this repository. The person reading this line is looking at a
//     browser certificate warning and deciding whether it is their own box or
//     something sitting in the middle of the connection. The fingerprint is the
//     only thing that answers that, and install.sh prints the matching
//     `/api/v1/tls/ca` URL at line 1058 as the documented way to clear the
//     warning. internal/tlsx tests CAFingerprint() as a function; nothing tested
//     that the startup path SHOWS it, and a deleted print is invisible to every
//     tlsx test.
//
//  2. THE PRE-ISSUANCE ARM IS A BRANCH SELECTION. `errors.Is(err,
//     tlsx.ErrNoCertificate)` distinguishes "acme has not issued yet, this is
//     normal, it will happen on the first https request" from "certificate
//     unreadable", which is an alarm. Collapsing the two turns every first boot
//     in acme mode into an apparent fault, which is the shape of support
//     ticket that gets a working install rolled back.
//
// Neither subtest touches the network. tlsx.New(ModeACME) builds an autocert
// manager and returns; issuance happens lazily on the first handshake, which is
// exactly why CertInfo has an ErrNoCertificate to return.
func TestReportTLSTellsTheOperatorWhatTheBrowserWillSee(t *testing.T) {
	t.Run("selfsigned shows the fingerprint and where to fetch the CA", func(t *testing.T) {
		dir := t.TempDir()
		provider, err := tlsx.New(tlsx.Options{
			Mode:     tlsx.ModeSelfSigned,
			Hostname: "studio.local",
			DataDir:  dir,
		})
		if err != nil {
			t.Fatalf("tlsx.New(selfsigned): %v", err)
		}
		fp := provider.CAFingerprint()
		if fp == "" {
			t.Fatal("the selfsigned provider reports no CA fingerprint, so the assertion " +
				"below would be vacuous")
		}

		cfg := config.Default()
		cfg.DataDir = dir
		cfg.TLS.Hostname = "studio.local"

		const shown = "studio.local"
		out := captureStdout(t, func() { reportTLS(cfg, provider, shown) })

		if !strings.Contains(out, fp) {
			t.Errorf("reportTLS printed:\n%s\nwant it to contain the CA fingerprint %q.\n\n"+
				"That string is what an operator staring at a browser certificate warning "+
				"compares against, and it is the only thing that distinguishes their own "+
				"box from something intercepting the connection. Nothing else in this "+
				"repository asserts that the startup path shows it.", out, fp)
		}
		if !strings.Contains(out, "trust https://"+shown+"/api/v1/tls/ca") {
			t.Errorf("reportTLS printed:\n%s\nwant it to name https://%s/api/v1/tls/ca.\n\n"+
				"install.sh prints the same URL (line 1058) as the documented way to clear "+
				"the warning. A fingerprint with no way to act on it tells the operator "+
				"they have a problem and not what to do about it.", out, shown)
		}
	})

	t.Run("acme before issuance says so rather than reporting a fault", func(t *testing.T) {
		dir := t.TempDir()
		provider, err := tlsx.New(tlsx.Options{
			Mode:      tlsx.ModeACME,
			Hostname:  "studio.example",
			ACMEEmail: "ops@studio.example",
			DataDir:   dir,
		})
		if err != nil {
			t.Fatalf("tlsx.New(acme): %v", err)
		}

		cfg := config.Default()
		cfg.DataDir = dir
		cfg.TLS.Hostname = "studio.example"

		out := captureStdout(t, func() { reportTLS(cfg, provider, "studio.example") })

		if !strings.Contains(out, "certificate not issued yet") {
			t.Errorf("reportTLS printed:\n%s\nwant \"certificate not issued yet\".", out)
		}
		if strings.Contains(out, "certificate unreadable") {
			t.Errorf("reportTLS printed:\n%s\nwhich reports the certificate as UNREADABLE.\n\n"+
				"On a first boot in acme mode there is no certificate yet and that is "+
				"normal -- autocert issues one on the first https request. This is the "+
				"errors.Is(err, tlsx.ErrNoCertificate) arm; without it every acme install "+
				"greets its operator with what reads as a fault, and the healthy state and "+
				"the broken state become indistinguishable at exactly the moment the "+
				"operator is deciding whether to roll back.", out)
		}
	})
}
