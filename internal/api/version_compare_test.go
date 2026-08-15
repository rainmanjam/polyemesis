package api

import "testing"

// A build from source must never be offered the tag it was built after.
//
// SEEN ON A FRESH INSTALL, which is how it was found: the banner read
//
//	polyemesis v0.6.0 is available. You are running v0.6.0-167-g346af01.
//
// and "Prepare update" would have staged a genuine downgrade. `git describe`
// emits `<tag>-<commits-since>-g<sha>`, parseSemver reads everything past the
// first hyphen as a pre-release, and semver says a release outranks any
// pre-release of the same numbers. Correct for `-rc1`. Exactly backwards for a
// commit count.
//
// The blast radius is the wrong way round, too: a source build is currently the
// only way to have 0.7.0's security fixes, so the operators being told they are
// behind are the ones who are most current.
func TestASourceBuildIsNotOfferedTheTagItWasBuiltAfter(t *testing.T) {
	tests := []struct {
		name            string
		latest, current string
		wantNewer       bool
		wantComparable  bool
		why             string
	}{
		{
			name:   "the bug: 167 commits after v0.6.0",
			latest: "v0.6.0", current: "v0.6.0-167-g346af01",
			wantNewer: false, wantComparable: false,
			why: "offering v0.6.0 here is a downgrade, and staging it would drop the fixes those commits carry",
		},
		{
			name:   "a dirty working tree is still a source build",
			latest: "v0.6.0", current: "v0.6.0-167-g346af01-dirty",
			wantNewer: false, wantComparable: false,
			why: "--dirty changes nothing about whether the build can be ordered against a feed",
		},
		{
			name:   "a later tag is still not comparable against a source build",
			latest: "v0.9.0", current: "v0.6.0-167-g346af01",
			wantNewer: false, wantComparable: false,
			why: "the build may be ahead of v0.6.0 and behind v0.9.0, and the string does not say which — so the honest answer is 'cannot tell', not a guess in either direction",
		},

		// The cases the fix must NOT break. A regex that swallowed these would
		// stop real upgrades being offered, which is a worse failure than the
		// one being fixed.
		{
			name:   "a real release candidate still yields to its release",
			latest: "v0.7.0", current: "v0.7.0-rc1",
			wantNewer: true, wantComparable: true,
			why: "rc1 has no commit count and no g<sha>; this is the ordinary semver case the pre-release rule exists for",
		},
		{
			name:   "an ordinary upgrade between releases",
			latest: "v0.7.0", current: "v0.6.0",
			wantNewer: true, wantComparable: true,
			why: "the everyday case; if this breaks nobody is ever told about a release",
		},
		{
			name:   "already current",
			latest: "v0.7.0", current: "v0.7.0",
			wantNewer: false, wantComparable: true,
			why: "comparable and equal — the banner stays silent because there is nothing newer, not because we could not tell",
		},
		{
			name:   "an older feed than the installed release",
			latest: "v0.6.0", current: "v0.7.0",
			wantNewer: false, wantComparable: true,
			why: "a stale or rolled-back feed must not offer a downgrade either",
		},
		{
			name:   "the dev default is not comparable",
			latest: "v0.7.0", current: "dev",
			wantNewer: false, wantComparable: false,
			why: "`dev` is main.version's zero value and parses as nothing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newer, comparable := newerThan(tt.latest, tt.current)
			if newer != tt.wantNewer || comparable != tt.wantComparable {
				t.Fatalf("newerThan(%q, %q) = newer:%v comparable:%v, want newer:%v comparable:%v\n  %s",
					tt.latest, tt.current, newer, comparable, tt.wantNewer, tt.wantComparable, tt.why)
			}
		})
	}
}

// The suffix matcher has to be narrow, or it eats real pre-releases.
//
// Kept separate from the table above because this is about the SHAPE the regex
// accepts rather than about ordering: a version that is wrongly called a source
// build stops being offered upgrades forever, silently, and nobody notices
// because the symptom is an absence.
func TestOnlyAGitDescribeSuffixCountsAsASourceBuild(t *testing.T) {
	source := []string{
		"167-g346af01",
		"1-gabcdef1",
		"0-g1234567", // git describe --long on the tag itself
		"167-g346af01-dirty",
		"12-g0123456789abcdef01234567890abcdef012345", // 40-char sha
	}
	notSource := []string{
		"rc1", "rc.1", "beta", "alpha.2", "0", "167", "g346af01",
		"167-346af01",  // no g
		"167-gzzzzzzz", // not hex
		"167-g34af0",   // too short to be a sha
		"",             // a plain release has no pre-release at all
	}

	for _, s := range source {
		if !isSourceBuild(s) {
			t.Errorf("%q is a git describe suffix and was not recognised as one, so this build\n"+
				"will keep being offered a downgrade to the tag it was built after", s)
		}
	}
	for _, s := range notSource {
		if isSourceBuild(s) {
			t.Errorf("%q was treated as a source build, so an install on this version will never\n"+
				"be told about a real release again — a silent failure, because the symptom\n"+
				"is a banner that does not appear", s)
		}
	}
}
