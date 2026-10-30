package obsbridge_test

import (
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/obsbridge"
)

func TestAValidConfigurationHasNoProblems(t *testing.T) {
	cfg := baseConfig("ws://192.168.1.20:4455")
	cfg.Scenes = append(cfg.Scenes, obsbridge.SceneRule{Scene: "BRB", Preset: "mic-only", Destinations: []int64{7}})
	if p := cfg.Problems(); len(p) != 0 {
		t.Fatalf("problems = %q", p)
	}
	// Tailscale's MagicDNS name and its CGNAT range are the normal remote
	// deployment, and must be accepted like a LAN address.
	for _, u := range []string{"ws://studio-pc.tail1234.ts.net:4455", "ws://100.101.102.103:4455", "wss://obs.example.org/ws"} {
		cfg.URL = u
		if p := cfg.Problems(); len(p) != 0 {
			t.Errorf("%s: problems = %q", u, p)
		}
	}
}

// A URL carrying a password is refused, and the refusal does not quote it --
// a 400 that echoed the input would be the very leak the refusal prevents.
func TestAnAddressCarryingCredentialsIsRefusedWithoutBeingQuoted(t *testing.T) {
	secret := "pw" + strings.Repeat("Z", 12)
	for _, u := range []string{
		"ws://user:" + secret + "@192.168.1.20:4455",
		"ws://192.168.1.20:4455/?password=" + secret,
		"http://192.168.1.20:4455#" + secret,
		"ws://%zz" + secret,
	} {
		cfg := baseConfig(u)
		probs := cfg.Problems()
		if len(probs) == 0 {
			t.Errorf("%q was accepted", u)
		}
		for _, p := range probs {
			if strings.Contains(p, secret) {
				t.Errorf("the refusal quotes the URL: %q", p)
			}
		}
	}
}

func TestStoppingOnStreamIsRefusedUntilTheConsequenceIsAcknowledged(t *testing.T) {
	cfg := baseConfig("")
	cfg.StopAcknowledged = false
	probs := strings.Join(cfg.Problems(), "\n")
	if !strings.Contains(probs, "YouTube broadcast") || !strings.Contains(probs, "stopAcknowledged") {
		t.Fatalf("problems = %q", probs)
	}
	cfg.StopOnStream = false
	if p := cfg.Problems(); len(p) != 0 {
		t.Fatalf("starting alone needs no acknowledgement; problems = %q", p)
	}
}

func TestSceneRulesAreCheckedAgainstThePresetsAndTheBridgesOwnList(t *testing.T) {
	cases := map[string]obsbridge.SceneRule{
		"unknown routing preset":                  {Scene: "A", Preset: "loudest-possible"},
		"names no scene":                          {Scene: "  ", Preset: "mic-only"},
		"is not one of the bridge's destinations": {Scene: "B", Preset: "mic-only", Destinations: []int64{99}},
		"already has a rule":                      {Scene: "Just Chatting", Preset: "mic-only"},
	}
	for want, rule := range cases {
		cfg := baseConfig("")
		cfg.Scenes = append(cfg.Scenes, rule)
		if p := strings.Join(cfg.Problems(), "\n"); !strings.Contains(p, want) {
			t.Errorf("%+v: problems = %q; want one containing %q", rule, p, want)
		}
	}
}

func TestTheBoundsAreEnforced(t *testing.T) {
	cfg := baseConfig("")
	cfg.StopGraceSeconds = obsbridge.MaxStopGraceSeconds + 1
	cfg.Destinations = []int64{7, 7}
	p := strings.Join(cfg.Problems(), "\n")
	for _, want := range []string{"stopGraceSeconds", "listed twice"} {
		if !strings.Contains(p, want) {
			t.Errorf("problems = %q; want %q", p, want)
		}
	}
	cfg = baseConfig("")
	cfg.Destinations = make([]int64, obsbridge.MaxDestinations+1)
	for i := range cfg.Destinations {
		cfg.Destinations[i] = int64(i + 1)
	}
	if p := strings.Join(cfg.Problems(), "\n"); !strings.Contains(p, "at most") {
		t.Errorf("problems = %q", p)
	}
}
