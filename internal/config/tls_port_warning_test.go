package config

import (
	"strings"
	"testing"
)

// TLS on, but not on 443.
//
// THE OPERATOR THIS EXISTS FOR NEVER RAN install.sh. That script asks -- "HTTPS
// is normally served on 443, so browsers reach it without a port" -- defaults
// the answer to yes, and grants CAP_NET_BIND_SERVICE in the unit it writes
// (scripts/install.sh:640-655, :875). Anyone who used it is already on 443 and
// will never see this line.
//
// The one who does is the operator whose unit, compose file or Ansible role was
// written by hand. Measured on this project's own test box: tls.mode was set to
// selfsigned, the unit still carried `--addr :8080` from an earlier deploy, and
// the result was a perfectly working HTTPS server that no browser reaches
// without a port. Nothing anywhere said so. It works, so nobody investigates --
// which is the whole shape of the defect.
//
// The cases below are the ones that decide whether the message is worth
// printing, and the two "silent" ones matter as much as the loud one: a warning
// that fires when TLS is off would be noise on the majority install, and one
// that fires on 443 would be noise on every correct install.
func TestTLSPortWarning(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want bool // want a warning
	}{
		{
			name: "tls on, port 8080 — the case this exists for",
			cfg:  Config{Addr: ":8080", TLS: TLS{Mode: ModeSelfSigned}},
			want: true,
		},
		{
			name: "tls on, port 8443 — still not what a browser assumes",
			cfg:  Config{Addr: "0.0.0.0:8443", TLS: TLS{Mode: ModeACME}},
			want: true,
		},
		{
			name: "tls on, port 443 — correct, and must stay silent",
			cfg:  Config{Addr: ":443", TLS: TLS{Mode: ModeSelfSigned}},
			want: false,
		},
		{
			// InsecureExposureWarning already says the thing that matters here,
			// and it says something stronger: passwords are crossing the network
			// in plaintext. Adding "also, wrong port" would bury it.
			name: "tls off — silent, because the port is not the problem",
			cfg:  Config{Addr: ":8080", TLS: TLS{Mode: ModeOff}},
			want: false,
		},
		{
			name: "tls off on 443 — still silent",
			cfg:  Config{Addr: ":443", TLS: TLS{Mode: ModeOff}},
			want: false,
		},
		{
			// An addr with no port at all cannot be judged, and guessing would
			// produce a warning naming a port the operator never wrote.
			name: "no port in addr — nothing to say",
			cfg:  Config{Addr: "", TLS: TLS{Mode: ModeSelfSigned}},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cfg.TLSPortWarning()
			if tt.want && got == "" {
				t.Fatalf("no warning for addr %q with tls.mode %q.\n"+
					"This is the configuration that serves working HTTPS on a port no\n"+
					"browser assumes: every visitor must type it, and redirects from\n"+
					"port 80 carry it too. Silence here is how it goes unnoticed.",
					tt.cfg.Addr, tt.cfg.TLS.Mode)
			}
			if !tt.want && got != "" {
				t.Fatalf("warned about addr %q with tls.mode %q, which is not a problem:\n  %s\n"+
					"A warning that fires on a correct install is noise, and noise is\n"+
					"how the real ones stop being read.", tt.cfg.Addr, tt.cfg.TLS.Mode, got)
			}
		})
	}
}

// The message has to name the port and the fix, or it is a complaint rather
// than a warning.
//
// An operator reading it at 3am needs three things without leaving the terminal:
// which port they are on, what to change, and -- the one that actually blocks
// people -- that a non-root service needs a capability to bind 443 at all.
// Leaving that last part out produces a second, more confusing failure:
// "permission denied" on a port they were just told to use.
func TestTLSPortWarningSaysWhatToDoAboutIt(t *testing.T) {
	got := Config{Addr: ":8080", TLS: TLS{Mode: ModeSelfSigned}}.TLSPortWarning()
	if got == "" {
		t.Fatal("no warning to inspect")
	}
	for _, want := range []string{
		":8080",                // the port they are actually on
		"443",                  // where it should be
		"addr",                 // the config key to change
		"CAP_NET_BIND_SERVICE", // the thing that bites a non-root service
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the warning never mentions %q, so it cannot be acted on without\n"+
				"searching the docs:\n  %s", want, got)
		}
	}
	// It must also leave room for the legitimate case, or an operator behind a
	// proxy terminating on 443 will read it as an error and "fix" a correct
	// setup.
	if !strings.Contains(got, "deliberate") && !strings.Contains(got, "front of this box") {
		t.Errorf("the warning does not allow for a non-standard port being intentional,\n"+
			"so it reads as a fault on a setup that is fine:\n  %s", got)
	}
}
