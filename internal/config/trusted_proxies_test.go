package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTrustedProxiesParseAddressesAndCIDRs(t *testing.T) {
	c := Config{TrustedProxies: []string{"172.16.0.0/12", " 10.1.2.3 ", "::ffff:192.0.2.1", "2001:db8::/32", "10.9.9.9/8"}}
	got, err := c.TrustedProxyPrefixes()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"172.16.0.0/12", "10.1.2.3/32", "192.0.2.1/32", "2001:db8::/32", "10.0.0.0/8"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i].String() != want[i] {
			t.Errorf("entry %d = %s, want %s", i, got[i], want[i])
		}
	}
}

// A typo in trustedProxies must stop the server, naming the entry: trusting
// "nobody" by accident is a silent throttle-sharing outage, and a near-miss
// CIDR is trusting somebody else.
func TestAMalformedTrustedProxyRefusesToStart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("trustProxyHeaders: true\ntrustedProxies: [\"172.17.0.300\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "172.17.0.300") {
		t.Fatalf("Load = %v, want an error naming the bad entry", err)
	}
	if _, err := (Config{TrustedProxies: []string{"10.0.0.0/33"}}).TrustedProxyPrefixes(); err == nil {
		t.Error("a /33 on IPv4 was accepted")
	}
}

func TestProxyHeaderWarning(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
		want []string // substrings; nil means no warning
	}{
		{"trust off", Config{Addr: ":8080"}, nil},
		{"behind a proxy on loopback, as documented", Config{Addr: "127.0.0.1:8080", TrustProxyHeaders: true}, nil},
		{"public bind from config.yaml", Config{Addr: ":8080", TrustProxyHeaders: true},
			[]string{":8080", "addr in config.yaml", "bind 127.0.0.1"}},
		{"public bind from --addr: names the unit", Config{Addr: ":8080", TrustProxyHeaders: true, AddrFromFlag: true},
			[]string{"--addr flag", "ExecStart"}},
		{"loopback, but serving TLS itself", Config{Addr: "127.0.0.1:443", TrustProxyHeaders: true,
			TLS: TLS{Mode: ModeSelfSigned, Hostname: "box.example"}}, []string{"terminates TLS itself", "selfsigned"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.cfg.ProxyHeaderWarning()
			if tc.want == nil {
				if got != "" {
					t.Fatalf("unexpected warning: %s", got)
				}
				return
			}
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("warning %q does not mention %q", got, w)
				}
			}
		})
	}
}
