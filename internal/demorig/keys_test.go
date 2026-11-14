package demorig

import (
	"strings"
	"testing"
)

// The server decodes request bodies with encoding/json, which matches an object
// key to a field ignoring case (Unicode simple folds included) and lets the last
// match win. Keys are therefore checked by exact spelling against an allow-list,
// so a case variant is an unknown key and is refused.

func TestCheckKeysRefusesEveryCaseVariantOfACheckedField(t *testing.T) {
	ok := map[string]any{"name": "n", "kind": "rtmp", "url": "rtmp://a.demo.invalid/x", "enabled": false}
	if err := CheckKeys("destination", ok, DestinationKeys); err != nil {
		t.Fatalf("a plain destination was refused: %v", err)
	}
	for _, key := range []string{
		"backupurl", "BackupUrl", "backupURL", "BACKUPURL",
		"Multitrack", "multitracK", // U+212A KELVIN SIGN folds to k
		"Enabled", "ENABLED", "AccountID", "accountid",
		"ExtraOutputArgs", "extraoutputargs", "Kind", "URL", "Url",
		"backupIngestWanted", "backupStreamKey", "somethingNew",
	} {
		obj := map[string]any{"name": "Spanish", "url": "x"}
		obj[key] = true
		err := CheckKeys("destination", obj, DestinationKeys)
		if err == nil {
			t.Errorf("key %q accepted", key)
			continue
		}
		if !strings.Contains(err.Error(), "destination") {
			t.Errorf("key %q: the error should say what was checked: %v", key, err)
		}
	}
}

func TestCheckKeysDescendsAndNeverNamesAValue(t *testing.T) {
	prof := map[string]any{"mode": "tracks", "tracks": []any{map[string]any{"track": 0, "enabled": true, "gain": 1.0}}}
	dest := map[string]any{"name": "n", "profile": prof}
	if err := CheckKeys("destination", dest, DestinationKeys); err != nil {
		t.Fatalf("a profile with tracks was refused: %v", err)
	}
	prof["tracks"] = []any{map[string]any{"track": 0, "Enabled": true}}
	if err := CheckKeys("destination", dest, DestinationKeys); err == nil {
		t.Error("a case variant inside a nested array was accepted")
	}
	// A scalar where an object is expected, and an object where a scalar is.
	if err := CheckKeys("destination", map[string]any{"name": map[string]any{"x": 1}}, DestinationKeys); err == nil {
		t.Error("an object in a scalar field was accepted")
	}
	err := CheckKeys("destination", map[string]any{"streamKey": "SECRETVALUE", "name": "n", "Zed": "SECRETVALUE"}, DestinationKeys)
	if err == nil || strings.Contains(err.Error(), "SECRETVALUE") {
		t.Errorf("the error must name the key, never a value: %v", err)
	}
}

func TestCheckFixtureObjectsCoversEveryObjectTheDriverSends(t *testing.T) {
	good := FixtureObjects{
		Settings:     map[string]any{"recording": map[string]any{"enabled": true}},
		Renditions:   []map[string]any{{"name": "r", "height": 720.0}},
		Destinations: []map[string]any{{"name": "d", "url": "x.mkv", "kind": "file"}},
		AlertRules:   []map[string]any{{"name": "a", "url": "https://h.demo.invalid/x", "events": []any{"e"}}},
		Schedules:    []map[string]any{{"name": "s", "action": "start", "destinations": []any{"d"}}},
	}
	if err := CheckFixtureObjects(good); err != nil {
		t.Fatalf("a good set was refused: %v", err)
	}
	for name, mut := range map[string]func(f *FixtureObjects){
		"settings":    func(f *FixtureObjects) { f.Settings["Recording"] = true },
		"settings in": func(f *FixtureObjects) { f.Settings["recording"] = map[string]any{"Enabled": true} },
		"mqtt":        func(f *FixtureObjects) { f.Settings["mqtt"] = map[string]any{"brokerUrl": "tcp://x"} },
		"rendition":   func(f *FixtureObjects) { f.Renditions[0]["Height"] = 1.0 },
		"destination": func(f *FixtureObjects) { f.Destinations[0]["backupurl"] = "rtmp://b.rtmp.youtube.com/x" },
		"alert rule":  func(f *FixtureObjects) { f.AlertRules[0]["Url"] = "https://example.com" },
		"schedule":    func(f *FixtureObjects) { f.Schedules[0]["Action"] = "start" },
	} {
		f := good
		f.Settings = map[string]any{"recording": map[string]any{"enabled": true}}
		f.Renditions = []map[string]any{{"name": "r", "height": 720.0}}
		f.Destinations = []map[string]any{{"name": "d", "url": "x.mkv", "kind": "file"}}
		f.AlertRules = []map[string]any{{"name": "a", "url": "https://h.demo.invalid/x"}}
		f.Schedules = []map[string]any{{"name": "s", "action": "start", "destinations": []any{"d"}}}
		mut(&f)
		if err := CheckFixtureObjects(f); err == nil {
			t.Errorf("%s: a variant or unknown key was accepted", name)
		}
	}
}

func TestCheckInvalidDestinationsRefusesAnEnabledNetworkDestination(t *testing.T) {
	err := CheckInvalidDestinations([]DestRow{{Name: "YouTube", Kind: "rtmp", URL: "rtmp://a.demo.invalid/x", Enabled: true}})
	if err == nil || !strings.Contains(err.Error(), "YouTube") || !strings.Contains(err.Error(), "enabled") {
		t.Errorf("an enabled network destination was accepted: %v", err)
	}
	if err := CheckInvalidDestinations([]DestRow{{Name: "Archive", Kind: "file", URL: "a.mkv", Enabled: true}}); err != nil {
		t.Errorf("an enabled file destination was refused: %v", err)
	}
}
