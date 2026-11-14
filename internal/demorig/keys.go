package demorig

import (
	"fmt"
	"sort"
	"strings"
)

// KeySet is an exact-spelling allow-list of the keys of one JSON object. Each
// key maps to the allow-list of its value when that is an object (or an array
// of objects); a nil value means the field is a scalar or a list of scalars.
type KeySet map[string]KeySet

// WHY KEYS ARE CHECKED BY EXACT SPELLING. The server decodes a request body
// with encoding/json, which matches an object key to a struct field ignoring
// case (Unicode simple folds included: "multitracK" with a Kelvin sign is
// "multitrack") and lets the last match win. A guard that reads "backupUrl" by
// that exact spelling therefore does not see "backupurl", which the server
// stores as the backup URL. So nothing the driver sends from a fixture is
// matched against field names: every key must be spelled exactly as one of the
// lists below, and a key that is not (a case variant, a typo, a field nobody
// reviewed) is refused before anything is created.

// DestinationKeys is a destination body as the fixtures write it. It names the
// fields the guards decide on (url, backupUrl, kind, enabled, the extra FFmpeg
// arguments, accountId, multitrack) and the ones a fixture needs; a field that
// is in neither list is refused.
var DestinationKeys = KeySet{
	"name": nil, "kind": nil, "platform": nil, "url": nil, "backupUrl": nil,
	"streamKey": nil, "enabled": nil, "audioBitrate": nil, "rendition": nil,
	"extraInputArgs": nil, "extraOutputArgs": nil, "accountId": nil, "multitrack": nil,
	"profile": {
		"mode": nil, "normalize": nil, "sampleRate": nil, "excludeRoles": nil,
		"loudness": {"targetLufs": nil, "truePeakDb": nil},
		"matrix":   {"channel": nil, "gain": nil, "out": nil, "track": nil},
		"tracks":   {"enabled": nil, "gain": nil, "track": nil},
	},
}

// RenditionKeys is a rendition body.
var RenditionKeys = KeySet{
	"name": nil, "note": nil, "width": nil, "height": nil, "fps": nil,
	"videoBitrate": nil, "maxrateKbps": nil, "bufsizeKbps": nil,
	"encoder": nil, "preset": nil, "gopSeconds": nil,
}

// AlertRuleKeys is an alert-rule body.
var AlertRuleKeys = KeySet{
	"name": nil, "url": nil, "format": nil, "minSeverity": nil, "events": nil,
	"debounceSeconds": nil, "minIntervalSeconds": nil, "enabled": nil,
}

// ScheduleKeys is a schedule body; `destinations` is the fixture's own list of
// names, which the driver replaces with ids.
var ScheduleKeys = KeySet{
	"name": nil, "action": nil, "kind": nil, "tz": nil, "atMinutes": nil,
	"days": nil, "graceSeconds": nil, "enabled": nil, "destinations": nil,
}

// SettingsKeys is the part of the settings document a fixture merges into the
// server's. Nothing that names a host (ingest pull, MQTT, a model endpoint) is
// in it.
var SettingsKeys = KeySet{
	"recording": {"enabled": nil, "maxAgeHours": nil, "maxGb": nil, "minFreeGb": nil, "segmentSeconds": nil},
	"playout":   {"enabled": nil},
	"preview":   {"enabled": nil},
	"automod": {
		"enabled": nil,
		"on": {
			"twitch/delete/rules": nil, "twitch/timeout/history": nil,
			"twitch/timeout/rules": nil, "youtube/delete/rules": nil,
		},
		"platformEnabled": {"twitch": nil, "youtube": nil},
		"rules": {
			"action": nil, "enabled": nil, "id": nil, "name": nil,
			"pattern": nil, "timeoutSeconds": nil,
		},
	},
}

// CheckKeys returns nil only if every key of obj, at every depth, is spelled
// exactly as allowed lists it. The error names what was checked and the
// offending key's path, never a value.
func CheckKeys(what string, obj any, allowed KeySet) error {
	var bad []string
	walkKeys(obj, allowed, "", &bad)
	if len(bad) == 0 {
		return nil
	}
	sort.Strings(bad)
	return fmt.Errorf("demo rig: %s: keys that are not spelled exactly as the server's fields (a case variant is a different key to this check and the same field to the server): %s", what, strings.Join(bad, ", "))
}

func walkKeys(v any, allowed KeySet, path string, bad *[]string) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			p := path + k
			sub, ok := allowed[k]
			if !ok {
				*bad = append(*bad, fmt.Sprintf("%q", p))
				continue
			}
			if sub == nil {
				if hasObject(val) {
					*bad = append(*bad, fmt.Sprintf("%q (an object where the server takes a value)", p))
				}
				continue
			}
			walkKeys(val, sub, p+".", bad)
		}
	case []any:
		for _, e := range x {
			walkKeys(e, allowed, path, bad)
		}
	}
}

func hasObject(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		return true
	case []any:
		for _, e := range x {
			if hasObject(e) {
				return true
			}
		}
	}
	return false
}

// FixtureObjects is everything the seed driver sends to the server that is
// taken from a fixture as a raw JSON object. Sources, the rolling buffer, clips
// and the sign-in are built by the driver from typed fields, so nothing raw of
// theirs reaches the server.
type FixtureObjects struct {
	Settings     map[string]any
	Renditions   []map[string]any
	Destinations []map[string]any
	AlertRules   []map[string]any
	Schedules    []map[string]any
}

// CheckFixtureObjects applies CheckKeys to each kind of object, so the driver
// and its tests ask one question.
func CheckFixtureObjects(f FixtureObjects) error {
	var errs []string
	add := func(what string, obj any, allowed KeySet) {
		if err := CheckKeys(what, obj, allowed); err != nil {
			errs = append(errs, strings.TrimPrefix(err.Error(), "demo rig: "))
		}
	}
	if f.Settings != nil {
		add("settings", f.Settings, SettingsKeys)
	}
	for i, o := range f.Renditions {
		add(fmt.Sprintf("rendition %d", i+1), o, RenditionKeys)
	}
	for i, o := range f.Destinations {
		add(fmt.Sprintf("destination %s", nameOf(o, i)), o, DestinationKeys)
	}
	for i, o := range f.AlertRules {
		add(fmt.Sprintf("alert rule %s", nameOf(o, i)), o, AlertRuleKeys)
	}
	for i, o := range f.Schedules {
		add(fmt.Sprintf("schedule %s", nameOf(o, i)), o, ScheduleKeys)
	}
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("demo rig: %s", strings.Join(errs, "; "))
}

func nameOf(o map[string]any, i int) string {
	if n, _ := o["name"].(string); n != "" {
		return fmt.Sprintf("%q", n)
	}
	return fmt.Sprintf("#%d", i+1)
}
