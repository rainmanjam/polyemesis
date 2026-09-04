package main

import (
	"flag"
	"path/filepath"
	"testing"
)

// THE RULE THAT SEPARATES A TYPO FROM A FRESH BOX. #644.
//
// An absent --config used to fall back to defaults, and the defaults are not a
// smaller version of the operator's install but a different one: new
// secret.key, empty database, plaintext on :8080, unauthenticated /setup
// reopened. The server came up looking healthy and sharing nothing with the
// install that was meant to start.
//
// The fix hangs on whether the flag was PASSED, not on what the path contains.
// These are the two halves of that, and the second is as important as the
// first: a guard that refuses everything would satisfy the first test alone
// while making every fresh install fail to boot.

func visitorFor(t *testing.T, args ...string) func(func(*flag.Flag)) {
	t.Helper()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.String("config", "polyemesis.yaml", "")
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parsing %v: %v", args, err)
	}
	return fs.Visit
}

func TestAnExplicitConfigThatIsAbsentRefusesToStart(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "typo.yaml")

	load := configLoaderFor(visitorFor(t, "-config", missing))
	if _, err := load(missing); err == nil {
		t.Fatal("an explicitly named config file that does not exist was accepted.\n" +
			"The server would boot on defaults: a new secret.key, an empty database and " +
			"plaintext on :8080 -- a different install that looks like a working one.")
	}
}

func TestTheDefaultConfigNameStillDefaults(t *testing.T) {
	// The control. Without it the fix could be "refuse whenever the file is
	// missing", which stops every fresh box from booting -- and would pass the
	// test above.
	missing := filepath.Join(t.TempDir(), "polyemesis.yaml")

	load := configLoaderFor(visitorFor(t)) // no -config passed
	if _, err := load(missing); err != nil {
		t.Fatalf("a fresh install with no config file and no --config flag refused to "+
			"start: %v", err)
	}
}

func TestPassingTheDefaultNameExplicitlyIsStillExplicit(t *testing.T) {
	// The case that rules out comparing the path against the default string
	// instead of asking whether the flag was set. An operator who types
	// `--config polyemesis.yaml` in a unit file is asserting the file exists,
	// and a silent fallback there is the same empty install by another route.
	missing := filepath.Join(t.TempDir(), "polyemesis.yaml")

	load := configLoaderFor(visitorFor(t, "-config", missing))
	if _, err := load(missing); err == nil {
		t.Fatal("--config naming a missing file was accepted because the name happened " +
			"to match the default. The decision must rest on the flag being set, not on " +
			"what it was set to.")
	}
}
