package playout

import (
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/testenv"
)

// tsBlock returns the body of `export interface <name> ... { ... }` or of
// `export type <name> = ...;` in src, failing when it is absent.
func tsBlock(t *testing.T, src, kind, name string) string {
	t.Helper()
	var re *regexp.Regexp
	if kind == "interface" {
		re = regexp.MustCompile(`(?s)export interface ` + name + `\b[^{]*\{(.*?)\n\}`)
	} else {
		re = regexp.MustCompile(`(?s)export type ` + name + `\s*=(.*?);`)
	}
	m := re.FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("ui/src/lib/types.ts declares no %s %s; the beacon contract is written once "+
			"in Go and once in TypeScript, and the TypeScript half is missing", kind, name)
	}
	return testenv.StripJSComments(m[1])
}

func jsonNames(rt reflect.Type) []string {
	var out []string
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		tag := strings.Split(f.Tag.Get("json"), ",")[0]
		if f.Anonymous && tag == "" {
			continue
		}
		if tag == "" || tag == "-" {
			continue
		}
		out = append(out, tag)
	}
	return out
}

// TestUITypesCanNameEveryBeaconField holds the two halves of the beacon
// contract together: every JSON field of the Go Beacon, QoETotals and
// QoESummary has a name in types.ts, and every enum value the server accepts is
// in the TypeScript union. Cross-platform analytics reads these; a field one
// side cannot name is a field the other side silently drops.
func TestUITypesCanNameEveryBeaconField(t *testing.T) {
	src := testenv.ReadUI(t, "lib", "types.ts")

	for _, c := range []struct {
		ts string
		rt reflect.Type
	}{
		{"BeaconV1", reflect.TypeOf(Beacon{})},
		{"PlayoutQoETotals", reflect.TypeOf(QoETotals{})},
		{"PlayoutQoESummary", reflect.TypeOf(QoESummary{})},
	} {
		body := tsBlock(t, src, "interface", c.ts)
		names := jsonNames(c.rt)
		if len(names) == 0 {
			t.Fatalf("%s has no JSON fields; this guard would check nothing", c.rt)
		}
		for _, n := range names {
			if !regexp.MustCompile(`(^|[{;,\s])` + regexp.QuoteMeta(n) + `\??\s*:`).MatchString(body) {
				t.Errorf("%s.%s (json %q) has no field in types.ts's %s", c.rt.Name(), n, n, c.ts)
			}
		}
	}

	errs := tsBlock(t, src, "type", "BeaconError")
	for _, e := range BeaconErrors {
		if !strings.Contains(errs, `"`+string(e)+`"`) {
			t.Errorf("error code %q is accepted by the server but not in types.ts's BeaconError", e)
		}
	}
	kinds := tsBlock(t, src, "type", "BeaconKind")
	for _, k := range []BeaconKind{BeaconStart, BeaconHeartbeat, BeaconEnd} {
		if !strings.Contains(kinds, `"`+string(k)+`"`) {
			t.Errorf("kind %q is accepted by the server but not in types.ts's BeaconKind", k)
		}
	}
	if !regexp.MustCompile(`\bv:\s*1\s*;`).MatchString(tsBlock(t, src, "interface", "BeaconV1")) {
		t.Errorf("types.ts's BeaconV1 does not pin v to %d", BeaconVersion)
	}
	states := tsBlock(t, src, "type", "PlayoutLiveState")
	for _, s := range []LiveState{LiveStateLive, LiveStateEnded, LiveStateOffline} {
		if !strings.Contains(states, `"`+string(s)+`"`) {
			t.Errorf("live state %q is not in types.ts's PlayoutLiveState", s)
		}
	}
}
