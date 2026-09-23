package main

import (
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/tlsx"
)

// trustProxyHeaders on a public listener is said at startup, and it names the
// --addr flag when that is what set the address: the unit's --addr :8080 beats
// addr: 127.0.0.1 in config.yaml, which is how a box that followed the proxy
// docs still ends up listening publicly.
func TestTheBannerWarnsWhenProxyHeadersAreTrustedOnAPublicListener(t *testing.T) {
	provider, err := tlsx.New(tlsx.Options{Mode: tlsx.ModeOff})
	if err != nil {
		t.Fatal(err)
	}
	cfg, store, tools := bannerFixture(t)
	cfg.TrustProxyHeaders = true
	cfg.Addr = ":8080"
	cfg.AddrFromFlag = true

	out := captureStdout(t, func() {
		if err := reportStartup(newLogger("error"), cfg, provider, store, tools); err != nil {
			t.Errorf("reportStartup: %v", err)
		}
	})
	if !strings.Contains(out, "WARNING: trustProxyHeaders is on") || !strings.Contains(out, "--addr flag") {
		t.Fatalf("banner does not warn about trusted proxy headers on a public --addr:\n%s", out)
	}

	cfg.Addr = "127.0.0.1:8080"
	out = captureStdout(t, func() {
		if err := reportStartup(newLogger("error"), cfg, provider, store, tools); err != nil {
			t.Errorf("reportStartup: %v", err)
		}
	})
	if strings.Contains(out, "trustProxyHeaders is on") {
		t.Fatalf("banner warns on the documented loopback-behind-a-proxy shape:\n%s", out)
	}
}
