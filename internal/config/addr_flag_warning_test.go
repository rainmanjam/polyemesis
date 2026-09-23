package config

import (
	"strings"
	"testing"
)

// THE WARNING TOLD THE OPERATOR TO EDIT A LINE THE FLAG OVERRIDES.
//
// TLSPortWarning and InsecureExposureWarning both say to change addr: in
// config.yaml. Every shipped unit and the image pass --addr, and main applies
// the flag after the file, so following the advice restarted the server onto
// the same port with the same warning. When the address came from the flag,
// the warning now says so and names where to change it. Staging-readiness
// row 25.
//
// Mutation: make addrFlagNote return "". Observed to fail on both warnings.
func TestAddrWarningsNameTheFlagWhenTheFlagSetTheAddress(t *testing.T) {
	tlsOn := Config{Addr: ":8443", TLS: TLS{Mode: ModeSelfSigned, Hostname: "box.lan"}}
	plain := Config{Addr: ":8080", TLS: TLS{Mode: ModeOff}}

	for _, tc := range []struct {
		name string
		warn func(Config) string
		cfg  Config
	}{
		{"TLSPortWarning", Config.TLSPortWarning, tlsOn},
		{"InsecureExposureWarning", Config.InsecureExposureWarning, plain},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fromFile := tc.warn(tc.cfg)
			if fromFile == "" {
				t.Fatal("no warning at all for this configuration; the fixture is wrong")
			}
			if strings.Contains(fromFile, "--addr") {
				t.Errorf("the address came from config.yaml, but the warning talks about --addr: %q", fromFile)
			}

			flagged := tc.cfg
			flagged.AddrFromFlag = true
			fromFlag := tc.warn(flagged)
			for _, want := range []string{"--addr", "overrides addr:", "ExecStart"} {
				if !strings.Contains(fromFlag, want) {
					t.Errorf("the address came from --addr, and the warning does not say %q, so "+
						"the operator edits config.yaml and nothing changes: %q", want, fromFlag)
				}
			}
		})
	}
}
