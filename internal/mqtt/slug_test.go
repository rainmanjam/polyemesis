package mqtt

import (
	"regexp"
	"strings"
	"testing"
)

// The charset a slug is allowed to contain: the intersection of what MQTT
// permits in a topic level and what Home Assistant permits in an object id.
var slugCharset = regexp.MustCompile(`^[a-z0-9_-]+$`)

func TestSlugStaysInsideTheAllowedCharset(t *testing.T) {
	names := []string{
		"Twitch (main)", "YouTube — backup", "", "   ", "!!!", "$SYS",
		"a/b", "wildcard+here", "hash#here", "NUL\x00byte", "Ünïcødé",
		"日本語", strings.Repeat("x", 300), "-leading", "trailing-",
		"multiple   spaces", "Mixed_Case-42",
	}
	for _, n := range names {
		got := Slug(n)
		if !slugCharset.MatchString(got) {
			t.Errorf("Slug(%q) = %q, which leaves the [a-z0-9_-] charset", n, got)
		}
		if got == "" {
			t.Errorf("Slug(%q) is empty; an empty topic level is a distinct topic no filter matches", n)
		}
	}
}

// The reason this package has a Slug function at all. Every pair below reduces
// to the same cleaned text, and a collision would mean one destination's
// retained state permanently overwriting another's.
func TestSlugSeparatesNamesThatCleanToTheSameText(t *testing.T) {
	pairs := [][2]string{
		{"Twitch (main)", "Twitch [main]"},
		{"Twitch (main)", "Twitch {main}"},
		{"a b", "a-b"},
		{"a.b", "a_b_"},
		{"", "   "},
		{"!!!", "???"},
		{"YouTube — main", "YouTube - main"},
		{"UPPER", "upper "},
		// The structural hole alreadySuffixed exists to close: a name that is
		// already shaped like a slug carrying a hash.
		{"twitch-1a2b3c4d", "Twitch!1a2b3c4d"},
	}
	for _, p := range pairs {
		a, b := Slug(p[0]), Slug(p[1])
		if a == b {
			t.Errorf("Slug(%q) and Slug(%q) both = %q; two names share one retained topic",
				p[0], p[1], a)
		}
	}
}

// A name that needs no alteration must survive untouched. Without this the
// suite would pass just as happily if Slug hashed everything, which would make
// every topic unreadable and every rename look like a new device.
func TestSlugLeavesAnAlreadyCleanNameAlone(t *testing.T) {
	for _, n := range []string{"twitch", "youtube-main", "a", "s3_archive", "cam1"} {
		if got := Slug(n); got != n {
			t.Errorf("Slug(%q) = %q, want it unchanged; a clean name must produce a readable topic", n, got)
		}
	}
}

func TestSlugIsDeterministic(t *testing.T) {
	// Retained topics are addressed by name ACROSS RESTARTS. A slug that varied
	// per process would orphan every topic on every restart and leave the
	// broker accumulating dead state forever.
	//
	// Pinned GOLDEN VALUES, and that is the whole point of this test rather
	// than a detail.
	//
	// It previously called Slug twice and compared the results to each other,
	// which cannot detect the failure this comment describes. A per-process
	// seed -- rand seeded at init, or Go's randomised map hash -- returns the
	// SAME value twice within one process and a different one after a restart,
	// so the old form passed while the exact hazard was live. Only a value
	// fixed in the source catches drift between processes.
	//
	// These are sha256-derived, so they are stable by construction. If one of
	// them changes, every retained topic and every Home Assistant entity id
	// already deployed changes with it -- that is a migration, not a test
	// failure to paper over.
	for _, tc := range []struct{ in, want string }{
		{"Twitch (main)", "twitch-main-3f17ba2c"},
		// Already safe, so it passes through untouched and carries no suffix.
		{"clean", "clean"},
		// Empty is not a valid topic segment, so it becomes a fixed stand-in.
		{"", "x-e3b0c442"},
		// A non-ASCII dash: substituted, not dropped, so two names that differ
		// only in punctuation cannot collide.
		{"Studio B — Feed 2", "studio-b-feed-2-af1bda19"},
		// Already shaped like a hashed slug, and hashed anyway -- otherwise a
		// name could impersonate another destination's topic.
		{"already-deadbeef", "already-deadbeef-8ba32f01"},
	} {
		if got := Slug(tc.in); got != tc.want {
			t.Errorf("Slug(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A brute-force check that the alteration rule holds across a large adversarial
// input set, rather than only across the hand-picked pairs above.
func TestSlugIsInjectiveOverAGeneratedCorpus(t *testing.T) {
	seen := map[string]string{}
	var names []string
	for _, base := range []string{"twitch", "youtube", "x", ""} {
		for _, deco := range []string{
			"", " ", "  ", "-", "_", ".", "!", "(a)", "[a]", "{a}", " a ", "/a",
			"—", "–", "+", "#", "$", "\x00", "A", "aA",
		} {
			names = append(names, base+deco, deco+base, base+deco+base)
		}
	}
	for _, n := range names {
		s := Slug(n)
		if prev, ok := seen[s]; ok && prev != n {
			t.Fatalf("Slug collision: %q and %q both produce %q", prev, n, s)
		}
		seen[s] = n
	}
	t.Logf("%d distinct names produced %d distinct slugs", len(names), len(seen))
}
