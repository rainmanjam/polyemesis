package db

// EVERY VARIABLE PIECE OF SQL IN THIS PACKAGE IS GENERATED, NOT WRITTEN.
//
// Sonar reports go:S2077 fourteen times across this package -- "make sure using
// a dynamically formatted SQL query is safe here". It is safe, and the reason is
// structural rather than a promise: the only non-constant fragment in any query
// here is an IN-clause placeholder list, and the single function that produces
// one can emit nothing but question marks and commas. Values never reach the
// SQL text; they go in as bound arguments.
//
// That was true by inspection and not by construction. jobs.go built the same
// "?,?,?" by hand in three places -- `ph[i] = "?"` in a loop, then
// strings.Join -- so the safety rested on three separate loops all continuing
// to assign the literal "?" and nothing else. A fourth site copying that shape
// and interpolating a caller's string would have looked exactly like its
// neighbours. Those three now call placeholders(), and this guard is what stops
// the shape coming back.
//
// WARNING rung, not Control. Control would be a query builder that cannot
// express concatenation at all -- a `type ConstSQL string` accepted by a wrapper
// around Query/Exec, so a runtime string is a compile error. That is a real
// option and a large one: it touches every call site in this package, and it is
// worth doing the day someone wants it. This guard costs nothing and closes the
// specific hole that existed.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Assigning the literal "?" into a slice is the hand-rolled placeholder loop.
// It is not wrong on its own -- it is wrong because it is a second way to do
// something that already has one way, and the second way is where the next
// mistake fits.
var handRolled = regexp.MustCompile(`(?m)^\s*(\w+\[\w+\]\s*=\s*"\?"|.*append\(\s*\w+\s*,\s*"\?"\s*\))`)

func TestNoHandRolledPlaceholderLists(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var scanned int
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		scanned++
		if m := handRolled.FindString(string(b)); m != "" {
			t.Errorf("%s builds an IN-clause placeholder list by hand:\n\n%s\n\n"+
				"Call placeholders(n) instead. It is the one function in this package "+
				"that produces a variable SQL fragment, and it can emit nothing but "+
				"question marks and commas -- which is the whole reason the fourteen "+
				"go:S2077 findings here are false positives. A second way to build the "+
				"same string is where the next interpolated value fits.", f, strings.TrimSpace(m))
		}
	}
	// A guard that scanned nothing passes for the wrong reason.
	if scanned < 5 {
		t.Fatalf("only %d non-test files scanned in internal/db; the glob is wrong "+
			"and this guard is not looking at the package it claims to guard", scanned)
	}
}

// And the helper it points at has to exist and behave, or the message above is
// advice to call something that is not there.
func TestPlaceholdersEmitsOnlyQuestionMarksAndCommas(t *testing.T) {
	for _, n := range []int{0, 1, 2, 5, 64} {
		got := placeholders(n)
		if n == 0 {
			if got != "" {
				t.Fatalf("placeholders(0) = %q, want empty", got)
			}
			continue
		}
		if strings.Trim(got, "?,") != "" {
			t.Fatalf("placeholders(%d) = %q, which contains something other than "+
				"'?' and ','. That is the property the whole package's SQL safety "+
				"rests on.", n, got)
		}
		if want := strings.Count(got, "?"); want != n {
			t.Fatalf("placeholders(%d) produced %d placeholders", n, want)
		}
	}
}
