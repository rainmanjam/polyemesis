// The registry's job is to hold true facts about other people's servers, so
// most of what is worth testing is the shape of the data rather than the code
// that reads it. The one piece of real logic -- AnalyseURL -- is tested against
// the URL that caused it to be written.

package services

import (
	"net/url"
	"strings"
	"testing"
)

// The defect this package exists for, stated as the URL that produced it.
// Kick's Stream Settings page shows exactly this, with no application path,
// and pasting it verbatim cost a live debugging session.
const kickDashboardURL = "rtmps://fa723fc1b171.global-contribute.live-video.net/"

// Proven able to fail against the committed tree by changing the guard in
// AnalyseURL from `strings.Trim(u.Path, "/") == ""` to `u.Path == ""`: the
// trailing slash Kick's dashboard prints makes Path "/" rather than "", the
// warning stops firing, and this test reports the URL as clean.
func TestAPathlessIngestURLIsReported(t *testing.T) {
	probs := AnalyseURL(kickDashboardURL)
	if len(probs) == 0 {
		t.Fatalf("no warning for %q, which is the URL Kick's dashboard hands you\n"+
			"and which cannot be published to.", kickDashboardURL)
	}
	if !strings.Contains(probs[0].Detail, "application path") {
		t.Errorf("the warning does not say what is wrong.\ngot: %s", probs[0].Detail)
	}
	// The offered fix has to be the URL that actually works, because an
	// operator will paste it back without checking.
	want := "rtmps://fa723fc1b171.global-contribute.live-video.net/app"
	if probs[0].Fix != want {
		t.Errorf("offered fix is not the working URL.\n got: %s\nwant: %s", probs[0].Fix, want)
	}
}

// The complement, and the one that stops the guard from being a nuisance:
// every URL the registry itself ships must pass it. A warning that fires on
// Twitch's own address would be trained away within a day.
func TestNoRegisteredServerTripsTheWarning(t *testing.T) {
	n := 0
	for _, svc := range All() {
		for _, srv := range svc.Servers {
			n++
			if probs := AnalyseURL(srv.URL); len(probs) > 0 {
				t.Errorf("%s / %s (%s) trips the warning polyemesis shows operators:\n  %s",
					svc.Name, srv.Name, srv.URL, probs[0].Detail)
			}
		}
	}
	if n == 0 {
		t.Fatal("no servers checked -- the registry did not load, and this test " +
			"would pass just as happily on an empty file")
	}
}

func TestPastingServerAndKeyIntoOneBoxIsReported(t *testing.T) {
	// The other half of the same confusion: the key ends up in the URL, and
	// then gets appended again from its own field.
	withKey := "rtmps://fa723fc1b171.global-contribute.live-video.net:443/" +
		"sk_us-west-2_ThisIsFiftySixCharactersOfStreamKeyMaterialXX"
	probs := AnalyseURL(withKey)
	found := false
	for _, p := range probs {
		if strings.Contains(p.Detail, "stream key") {
			found = true
		}
	}
	if !found {
		t.Errorf("a stream key pasted into the server box was not reported.\ngot: %+v", probs)
	}
}

func TestAnalyseURLIsSilentOnWhatItCannotJudge(t *testing.T) {
	// Validate owns scheme refusal. Two guards saying the same thing in
	// different words is how they drift apart.
	for _, in := range []string{"", "   ", "srt://host:9000?streamid=x", "file.flv", "://"} {
		if probs := AnalyseURL(in); len(probs) > 0 {
			t.Errorf("AnalyseURL(%q) should stay silent, got %+v", in, probs)
		}
	}
}

// Every service must be usable: an entry with no servers and no explanation is
// a dead end in the UI, and the operator gets a dropdown item that does
// nothing.
func TestEveryServiceIsEitherPickableOrExplained(t *testing.T) {
	all := All()
	if len(all) < 4 {
		t.Fatalf("registry has %d services; expected at least the four the "+
			"multistream suite exercises", len(all))
	}
	for _, s := range all {
		if s.ID == "" || s.Name == "" {
			t.Errorf("service %+v has no id or no name", s)
		}
		switch {
		case len(s.Servers) > 0:
		case s.PerChannelIngest && strings.TrimSpace(s.Note) != "":
		default:
			t.Errorf("%s offers no servers and no note explaining why, so the "+
				"operator who picks it is left with an empty URL box.", s.Name)
		}
		for _, srv := range s.Servers {
			if _, err := url.Parse(srv.URL); err != nil {
				t.Errorf("%s / %s has an unparseable URL %q: %v", s.Name, srv.Name, srv.URL, err)
			}
		}
	}
}

// The ceilings are the reason to carry Recommended at all, so an entry that
// silently has none would make CheckEncoder a no-op for that platform.
func TestEveryServicePublishesAnAudioCeiling(t *testing.T) {
	for _, s := range All() {
		if s.Recommended.MaxAudioKbps <= 0 {
			t.Errorf("%s has no max audio bitrate, so CheckEncoder can never "+
				"warn about it -- the field exists to be compared against.", s.Name)
		}
		if s.Recommended.KeyintSeconds <= 0 {
			t.Errorf("%s has no keyframe interval; every platform in OBS's data "+
				"publishes one (universally 2s).", s.Name)
		}
	}
}

func TestCheckEncoderComparesAgainstThePlatformNotAConstant(t *testing.T) {
	yt, ok := Lookup("youtube")
	if !ok {
		t.Fatal("youtube missing from the registry")
	}
	tw, ok := Lookup("twitch")
	if !ok {
		t.Fatal("twitch missing from the registry")
	}
	// 256 kbps is over YouTube's 160 and under Twitch's 320. A guard using a
	// single hardcoded ceiling would treat these two identically, and this is
	// the pair that catches it.
	if got := CheckEncoder(yt, 256, 0, 0); len(got) == 0 {
		t.Errorf("256 kbps audio should exceed YouTube's %d", yt.Recommended.MaxAudioKbps)
	}
	if got := CheckEncoder(tw, 256, 0, 0); len(got) != 0 {
		t.Errorf("256 kbps audio is within Twitch's %d, but was reported: %+v",
			tw.Recommended.MaxAudioKbps, got)
	}
}

func TestAnUnknownPlatformGetsNoOpinion(t *testing.T) {
	// A custom destination is legitimate. Lookup failing must not be an error
	// path, or every custom RTMP target starts producing warnings.
	if _, ok := Lookup("custom"); ok {
		t.Error("custom should have no registry entry")
	}
	if got := CheckEncoder(Service{}, 999, 999999, 999); len(got) != 0 {
		t.Errorf("a service with no published ceilings must produce no warnings, got %+v", got)
	}
}

func TestProvenanceIsRecorded(t *testing.T) {
	// This table is copied from somebody else's measurements. Saying so is
	// the difference between a citation and a claim.
	if !strings.Contains(Provenance(), "obs-studio") {
		t.Errorf("the registry does not record where its data came from.\ngot: %q", Provenance())
	}
}

// The registry is seeded from OBS's file, and OBS's file carries adult cam
// platforms among its ~200 services. Regenerating this registry wholesale from
// that source would pull them in silently, so the allowed set is written down
// here rather than left to whoever runs the script next.
//
// This is a product decision, not a technical one: those platforms issue a
// per-account ingest URL and key anyway, so a preset saves nobody any typing.
// Anyone publishing to one enters the URL and key by hand, exactly as they
// would for any endpoint polyemesis has never heard of.
//
// Proven able to fail against the committed tree by adding a service with
// id "camsoda" to services.json.
func TestTheRegistryStaysCurated(t *testing.T) {
	allowed := map[string]bool{
		"twitch": true, "youtube": true, "facebook": true, "kick": true,
	}
	for _, s := range All() {
		if !allowed[s.ID] {
			t.Errorf("service %q (%s) is not in the curated set.\n"+
				"Adding one is a deliberate act: put it in `allowed` above and "+
				"say why in the commit. Re-seeding from OBS wholesale is not, "+
				"because that file carries platforms this project does not ship "+
				"a preset for.", s.ID, s.Name)
		}
	}
}
