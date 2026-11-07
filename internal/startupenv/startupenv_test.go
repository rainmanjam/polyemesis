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

// X Livestream is the one switch that is on by default: unset or empty is on,
// the explicit false spellings turn it off, and a value that is neither a
// recognised on nor a recognised off follows Flag (off), so a typo cannot read
// as on.
func TestXLivestreamIsOnUnlessTurnedOff(t *testing.T) {
	live := func(m map[string]string) bool { return FromLookup(lookup(m)).XLivestream }
	if !live(nil) || !live(map[string]string{XLivestreamVar: ""}) || !live(map[string]string{XLivestreamVar: "  "}) {
		t.Error("an unset or empty POLYEMESIS_X_LIVESTREAM must leave X Livestream on")
	}
	for _, on := range []string{"1", "true", "TRUE", " True ", "1\n"} {
		if !live(map[string]string{XLivestreamVar: on}) {
			t.Errorf("POLYEMESIS_X_LIVESTREAM=%q turned X Livestream off", on)
		}
	}
	for _, off := range []string{"0", "false", "FALSE", " Off ", "no", "No\n", "yes", "on", "2", "truee", "flase"} {
		if live(map[string]string{XLivestreamVar: off}) {
			t.Errorf("POLYEMESIS_X_LIVESTREAM=%q left X Livestream on", off)
		}
	}
}

func TestTheSwitchesAreIndependent(t *testing.T) {
	e := FromLookup(lookup(map[string]string{FacebookCaptionsVar: "true"}))
	if !e.FacebookCaptions || e.XPublish || !e.XLivestream {
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
	e := FromLookup(lookup(map[string]string{XAppBearerVar: "s3cret-bearer", RumbleChatKeyVar: "s3cret-key", SetupCodeVar: "s3cret-code"}))
	for _, out := range []string{e.String(), e.GoString()} {
		for _, secret := range []string{"s3cret-bearer", "s3cret-key", "s3cret-code"} {
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

func TestFFmpegSettingsAreReadOnceAndTrimmed(t *testing.T) {
	e := FromLookup(lookup(map[string]string{
		FFmpegLogLevelVar:    " debug\n",
		IngestCaptureVar:     " /tmp/capture dir/x.ts ",
		FFmpegAssumeMajorVar: " 8 ",
	}))
	if e.FFmpegLogLevel != "debug" || e.IngestCapture != " /tmp/capture dir/x.ts " || e.FFmpegAssumeMajor != "8" {
		t.Errorf("FFmpeg settings not read: %+v", e)
	}
	if z := FromLookup(lookup(nil)); z.FFmpegLogLevel != "" || z.IngestCapture != "" || z.FFmpegAssumeMajor != "" {
		t.Errorf("an unset environment must leave FFmpeg alone: %+v", z)
	}
}
