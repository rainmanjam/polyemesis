package engine

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// publishSites finds every place this package starts a supervised process and
// reports whether the shutdown latch is read in the critical section that
// published it.
//
// SOURCE-SCANNING, WITH ITS EYES OPEN. A check that reads source is only as
// good as its ability to still find the thing it counts, so two things guard
// it: the caller asserts a floor on the number of sites found, and
// TestThePublishScannerCanTellTheTwoShapesApart below runs it over fixtures of
// both shapes. A scanner that quietly matched nothing would otherwise pass for
// ever while checking nothing, which is the failure mode this whole class of
// guard is prone to.
//
// The rule it applies is the one reconcileRecorder documents: between taking
// e.mu and calling Start, the code must have read e.stopped. The window
// searched is the critical section plus the Start that follows it, because that
// is the span in which the decision is made.
type publishSite struct {
	where   string
	guarded bool
}

var (
	startRe   = regexp.MustCompile(`^\s*\w+(\.\w+)*\.Start\(\)\s*$`)
	lockRe    = regexp.MustCompile(`^\s*e\.mu\.Lock\(\)\s*$`)
	stoppedRe = regexp.MustCompile(`\be\.stopped\b`)
	funcRe    = regexp.MustCompile(`^func (\([^)]*\) )?(\w+)`)
)

func publishSites(t *testing.T) []publishSite {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}

	var sites []publishSite
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		lines := strings.Split(string(raw), "\n")
		fn := ""
		for i, l := range lines {
			if m := funcRe.FindStringSubmatch(l); m != nil {
				fn = m[2]
			}
			if !startRe.MatchString(l) {
				continue
			}
			// Walk back to the e.mu.Lock() that opened the section this Start
			// belongs to. Bounded, because a Start with no lock above it in the
			// same function is not a publish-then-start at all -- Process.Start
			// on something already published, say -- and must not be reported.
			lo := -1
			for j := i; j >= 0 && i-j < 40; j-- {
				if funcRe.MatchString(lines[j]) && j != i {
					break
				}
				if lockRe.MatchString(lines[j]) {
					lo = j
					break
				}
			}
			if lo < 0 {
				continue
			}
			sites = append(sites, publishSite{
				where:   fn + " (" + f + ":" + itoa(i+1) + ")",
				guarded: stoppedRe.MatchString(strings.Join(lines[lo:i+1], "\n")),
			})
		}
	}
	return sites
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// The scanner's own positive and negative controls. Without these, a regexp
// that stopped matching would report zero unguarded sites and read as good
// news -- and the floor assertion in the caller only catches it going to zero
// ENTIRELY, not it losing the ability to see the unguarded shape specifically.
func TestThePublishScannerCanTellTheTwoShapesApart(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("guarded.go", `package engine

func (e *Engine) guardedOne() {
	proc := build()
	e.mu.Lock()
	if e.stopped {
		e.mu.Unlock()
		return
	}
	e.meters = proc
	e.mu.Unlock()
	proc.Start()
}
`)
	write("bare.go", `package engine

func (e *Engine) bareOne() {
	proc := build()
	e.mu.Lock()
	e.meters = proc
	e.mu.Unlock()
	proc.Start()
}
`)
	// A Start with no publishing lock above it: Process.Start on something
	// already published. Must not be reported at all, in either column.
	write("neither.go", `package engine

func (e *Engine) restartSomething() {
	e.existing.Start()
}
`)

	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	sites := publishSites(t)
	if len(sites) != 2 {
		t.Fatalf("scanner found %d sites in a fixture with exactly two publishes and "+
			"one bare Start: %+v", len(sites), sites)
	}
	byName := map[string]bool{}
	for _, s := range sites {
		byName[strings.SplitN(s.where, " ", 2)[0]] = s.guarded
	}
	if g, ok := byName["guardedOne"]; !ok || !g {
		t.Errorf("the guarded shape was not recognised as guarded (%v)", byName)
	}
	if g, ok := byName["bareOne"]; !ok || g {
		t.Errorf("the unguarded shape was not recognised as unguarded (%v)", byName)
	}
}
