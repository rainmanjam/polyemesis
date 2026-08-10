// Package testenv is the only place in this repository allowed to skip a test
// that would otherwise have something to say.
//
// #161. The excuse registry in internal/api has no jurisdiction over t.Skip, and
// t.Skip is the same free pass in a different shape: a test that declines to run
// prints "ok" and is counted as coverage by everything that counts coverage.
// Most of the ~95 skip sites in this repository are environmental -- no FFmpeg,
// -short, no GPU -- and those are honest. A handful are not:
//
//	"Twitch's scope version has moved; this case needs updating"
//	"the Facebook caveat has been reworded; nothing to assert"
//	"Kick is not in Providers() yet"
//	"DefaultSource now yields %d tracks; the hazard this documents is gone"
//	"the fixture started no destination, so there is nothing for the ..."
//
// Every one of those fires BECAUSE the thing under test changed. A skip that
// fires on drift is a test that deletes itself on the day it matters, and does
// it quietly. Six of them are gone entirely -- see internal/oauth's goldens --
// and the rest that need domain judgment come through Quarantine, where they are
// enumerated, counted, ceiling-clamped, and PRINTED BY NAME on every run.
//
// The distinction this package draws is not "skipped vs not". It is:
//
//	environmental   the machine cannot run this. Honest, and unmigrated this
//	                round -- the AST ratchet in this package's tests freezes
//	                their count so no NEW bare skip can land anywhere.
//	quarantined     the test has something to say and is not saying it. Visible,
//	                counted, and the target is zero.
package testenv

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"testing"
)

// The register is EMBEDDED rather than read from disk, so Quarantine works
// identically from any package. os.ReadFile in a test resolves against that
// test's own directory, which would have made this helper usable only from
// internal/testenv -- and a guard usable from one package is a guard.
//
//go:embed testdata/quarantine.json
var registerJSON []byte

// QuarantinePath is the committed register, relative to this package.
const QuarantinePath = "testdata/quarantine.json"

// QuarantineEntry is one silenced test, and every field is something a reviewer
// needs in order to delete it.
type QuarantineEntry struct {
	ID string `json:"id"`
	// Site is package/file:line at the time of filing, so a reader can find it
	// even after the function is renamed.
	Site string `json:"site"`
	// Why is what this test would be asserting if it ran.
	Why string `json:"why"`
	// WhatItWouldTake is the concrete work. "Unknown" is not an acceptable
	// value; if nobody knows, the test should be deleted rather than parked.
	WhatItWouldTake string `json:"whatItWouldTake"`
	// Issue is a citation for a reader. It discharges nothing -- the same rule
	// the route coverage ledger learned the hard way.
	Issue string `json:"issue,omitempty"`
}

type quarantineFile struct {
	Note string `json:"note"`
	// Ceiling is the ratchet. It may fall freely on regeneration and rising
	// takes a hand edit, which is a reviewable act.
	Ceiling int               `json:"ceiling"`
	Entries []QuarantineEntry `json:"entries"`
}

var (
	loadOnce sync.Once
	loaded   map[string]QuarantineEntry
	loadErr  error
)

func load() (map[string]QuarantineEntry, error) {
	loadOnce.Do(func() {
		var f quarantineFile
		if err := json.Unmarshal(registerJSON, &f); err != nil {
			loadErr = err
			return
		}
		loaded = map[string]QuarantineEntry{}
		for _, e := range f.Entries {
			loaded[e.ID] = e
		}
	})
	return loaded, loadErr
}

// Quarantine skips the calling test, loudly, and ONLY for an id that is in the
// committed register.
//
// It is deliberately more expensive than t.Skip. An id that is not registered
// FAILS the test rather than skipping it, so a quarantine cannot be created by
// typing a string at the call site -- which is the whole difference between this
// and the thing it replaces. Registering one means editing a committed file,
// stating what the test would assert and what it would take to un-silence it,
// and raising a ceiling by hand.
func Quarantine(t testing.TB, id string) {
	t.Helper()
	reg, err := load()
	if err != nil {
		t.Fatalf("quarantine %q: cannot parse %s: %v. A test cannot silence itself "+
			"while the register that authorises it is unreadable.", id, QuarantinePath, err)
	}
	e, ok := reg[id]
	if !ok {
		t.Fatalf("quarantine %q is not in %s. A quarantine is not created by typing a "+
			"string at the call site: register it, saying what this test would assert "+
			"and what it would take to un-silence it, and raise the ceiling by hand. "+
			"If you cannot write those two sentences, delete the test instead of "+
			"parking it.", id, QuarantinePath)
	}
	// Printed on EVERY run, by name, whether or not anybody asked for -v. A
	// quarantine nobody sees is the skip it replaced.
	fmt.Printf("QUARANTINED %s (%s): %s -- to un-silence: %s\n", e.ID, e.Site, e.Why, e.WhatItWouldTake)
	t.Skipf("QUARANTINED %s: %s", e.ID, e.Why)
}

// Entries returns the register, sorted, for the enumeration test.
func Entries() ([]QuarantineEntry, int, error) {
	var f quarantineFile
	if err := json.Unmarshal(registerJSON, &f); err != nil {
		return nil, 0, err
	}
	sort.Slice(f.Entries, func(i, j int) bool { return f.Entries[i].ID < f.Entries[j].ID })
	return f.Entries, f.Ceiling, nil
}
