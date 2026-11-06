package startupenv

import "testing"

func lookup(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// One parsing rule for every boolean switch. POLYEMESIS_CAPTIONS_FACEBOOK used
// to accept only the exact string "1" while the X switches accepted "1" and
// "true", so =true silently left Facebook captions off.
func TestEveryBooleanSwitchAcceptsTheSameSpellings(t *testing.T) {
	switches := map[string]func(Env) bool{
		XPublishVar:         func(e Env) bool { return e.XPublish },
		XLivestreamVar:      func(e Env) bool { return e.XLivestream },
		FacebookCaptionsVar: func(e Env) bool { return e.FacebookCaptions },
	}
	for name, got := range switches {
		for _, on := range []string{"1", "true", "TRUE", " True ", "1\n"} {
			if !got(FromLookup(lookup(map[string]string{name: on}))) {
				t.Errorf("%s=%q left the switch off", name, on)
			}
		}
		for _, off := range []string{"", "0", "false", "yes", "on", "2", "truee"} {
			if got(FromLookup(lookup(map[string]string{name: off}))) {
				t.Errorf("%s=%q turned the switch on", name, off)
			}
		}
	}
}

func TestTheSwitchesAreIndependent(t *testing.T) {
	e := FromLookup(lookup(map[string]string{FacebookCaptionsVar: "true"}))
	if !e.FacebookCaptions || e.XPublish || e.XLivestream {
		t.Errorf("one switch moved others: %+v", e)
	}
}

func TestStringValuesAreTrimmed(t *testing.T) {
	e := FromLookup(lookup(map[string]string{
		XAppBearerVar:        " bearer ",
		RumbleChatChannelVar: " chan ",
		RumbleChatKeyVar:     " key\n",
	}))
	if e.XAppBearer != "bearer" || e.RumbleChatChannel != "chan" || e.RumbleChatKey != "key" {
		t.Errorf("values not trimmed: bearer=%q channel=%q", e.XAppBearer, e.RumbleChatChannel)
	}
}

// The Rumble key and the X bearer are secrets: a struct printed into a log or an
// error must not carry them.
func TestPrintingTheEnvDoesNotLeakASecret(t *testing.T) {
	e := FromLookup(lookup(map[string]string{XAppBearerVar: "s3cret-bearer", RumbleChatKeyVar: "s3cret-key"}))
	for _, out := range []string{e.String(), e.GoString()} {
		for _, secret := range []string{"s3cret-bearer", "s3cret-key"} {
			if contains(out, secret) {
				t.Errorf("%q leaks %q", out, secret)
			}
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
