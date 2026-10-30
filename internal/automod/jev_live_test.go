package automod

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// jevLiveKeyEnv names the variable an operator sets to run the one test here
// that talks to the real, hosted Jev. It is read from the environment and
// nowhere else: there is no key in this repository, in a fixture, or in any
// image, and the test never prints it.
const jevLiveKeyEnv = "POLYEMESIS_JEV_LIVE_KEY"

// The real service, through the production connector, with the three canned
// probe lines -- which are synthetic and written in probe.go. Opt-in, because
// it costs money (about $0.0001), needs a key the repository does not have,
// and sends text to a hosted third party in the US.
//
// What it proves that no fake can: that the request this file builds is one
// the real API accepts, that the reply decodes into a verdict, and that the
// harmless line clears the flag band while the abusive one clears the floor.
func TestJevLiveProbeAgainstTheHostedService(t *testing.T) {
	key := os.Getenv(jevLiveKeyEnv)
	if key == "" {
		t.Skip("set " + jevLiveKeyEnv + " to run against the hosted Jev; nothing else here needs a network")
	}
	p, _ := PresetByID("jev")
	j := NewJev(JevConfig{
		Enabled: true, Endpoint: p.Endpoint, Model: p.Model, APIKey: key,
		KeyBinding: db.NormalizeEndpoint(p.Endpoint),
		// Longer than the production 4 s, because the first call after idle
		// opens a cold TLS connection (2.1 s measured) and this test makes
		// only three.
		Timeout:       10 * time.Second,
		Action:        ActionFlag,
		MinConfidence: 0.8,
		Instruction:   DefaultModelConfig().Instruction,
		Preset:        p.ID,
	}, nil)
	res := Probe(context.Background(), j)
	for _, c := range res.Cases {
		t.Logf("case=%s ok=%v flagged=%v action=%s category=%s p(act)=%.2f latency=%dms err=%q",
			c.Case, c.OK, c.Flagged, c.Action, c.Category, c.Confidence, c.LatencyMs, c.Error)
	}
	if !res.Passed {
		t.Fatalf("the hosted Jev did not pass the probe: %s", res.Problem)
	}
}
