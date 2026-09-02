package testenv

// A COMMENT THAT QUOTES A NUMBER IS A COPY OF THAT NUMBER, and copies go stale.
//
// ci.yml's go job argues about ordering constantly -- Go's `-timeout` has to
// fire before the job ceiling, or a hang reports a bare "cancelled" and names
// nothing -- and every one of those arguments quotes the ceiling by value. #637
// raised the go job from 25 to 35 and moved the one line that sets it. Four
// comments went on asserting 25, and one of them was the note explaining WHY
// each of the four shell steps carries a step timeout: "behind this job's
// timeout-minutes: 25 a hang here costs 25 minutes". A reader sizing a fifth
// step against that gets the wrong blast radius, with the authority of a
// comment that looks maintained.
//
// TestGoJobCeilingClearsItsTestDeadline (ci_timeout_test.go) already pins the
// arithmetic of the LIVE value. Nothing pinned the prose about it, and prose is
// what the next person reads before they touch the number.
//
// Detection rung, not Control: this finds the stale copy on the next CI run. A
// control would mean the comments not quoting the number at all -- YAML has no
// way to interpolate into a comment, and deleting the numbers would make the
// notes worse, since the whole argument is about two values in a relationship.
//
// SCOPE, deliberately narrow. Only two spellings are checked, both of which
// name a job's own ceiling explicitly:
//
//	# ... this job's timeout-minutes: 25 ...
//	# ... below the job's 25 ...
//
// Historical narrative is left alone on purpose -- ci.yml's header records that
// "the old value compared was 25 against 20", which is true and must stay true
// when the live value changes again. A guard that went red on its own change
// log is a guard somebody deletes.

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var (
	// A job header: exactly two spaces of indent, a name, a colon, nothing else.
	ceilingJobRe = regexp.MustCompile(`^  ([A-Za-z0-9_-]+):\s*$`)
	// The job's real ceiling, at the job's own indent rather than a step's.
	ceilingValueRe = regexp.MustCompile(`^    timeout-minutes:\s*(\d+)\s*$`)
	// The two spellings above, inside a comment line.
	//
	// ANCHORED ON "this"/"the", which is not pedantry: flake-rate.yml says
	// "Lower than the acceptance job's 120", naming a DIFFERENT job's ceiling
	// from inside a job with its own. A guard that read that as a claim about
	// the enclosing job would go red on a correct comment, and a guard whose
	// false alarms outnumber its catches is one people learn to ignore.
	ceilingClaimRe = regexp.MustCompile(`\b(?:this|the) job's(?: own)?(?: timeout-minutes:)?\s*(\d+)\b`)
)

func TestWorkflowCommentsDoNotQuoteAStaleJobCeiling(t *testing.T) {
	root := repoRootFromTest(t)
	dir := filepath.Join(root, ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}

	claimsChecked := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yml") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("reading %s: %v", e.Name(), err)
		}
		lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")

		// Two passes, because a comment that quotes the ceiling usually sits
		// hundreds of lines BELOW the `timeout-minutes:` it is talking about,
		// and several sit above it. Pass one maps every line to its job and
		// records that job's real ceiling; pass two reads the claims.
		jobOf := make([]string, len(lines))
		ceiling := map[string]int{}
		current := ""
		for i, ln := range lines {
			if m := ceilingJobRe.FindStringSubmatch(ln); m != nil && !strings.HasPrefix(strings.TrimSpace(ln), "#") {
				current = m[1]
			}
			jobOf[i] = current
			if m := ceilingValueRe.FindStringSubmatch(ln); m != nil && current != "" {
				if _, seen := ceiling[current]; !seen {
					ceiling[current], _ = strconv.Atoi(m[1])
				}
			}
		}

		for i, ln := range lines {
			trimmed := strings.TrimSpace(ln)
			if !strings.HasPrefix(trimmed, "#") {
				continue
			}
			m := ceilingClaimRe.FindStringSubmatch(trimmed)
			if m == nil {
				continue
			}
			job := jobOf[i]
			want, ok := ceiling[job]
			if !ok {
				// A comment naming "the job's N" in a job with no ceiling of its
				// own. Nothing to compare against; not this test's business.
				continue
			}
			claimsChecked++
			got, _ := strconv.Atoi(m[1])
			if got == want {
				continue
			}
			t.Errorf("%s:%d: this comment says the `%s` job's ceiling is %d; it is %d.\n\n    %s\n\n"+
				"The comments in these files argue about ORDERING -- Go's own -timeout has to "+
				"fire below the job ceiling, or a hang reports a bare \"cancelled\" and names "+
				"nothing -- and they make that argument by quoting the ceiling. A stale copy "+
				"sends the next person sizing a step timeout to the wrong blast radius, with "+
				"the authority of a comment that looks maintained. #637 raised this job from "+
				"25 to 35 and left four such copies behind.\n\n"+
				"Update the comment to %d, or, if you are describing what the value USED to "+
				"be, say so in words rather than in the two spellings this guard reads "+
				"(`the job's N`, `this job's timeout-minutes: N`).",
				e.Name(), i+1, job, got, want, trimmed, want)
		}
	}

	// The guard's own liveness. Every one of these claims lives in ci.yml's go
	// job today; if the parsing above breaks, or the comments are reworded into
	// spellings it cannot read, it starts passing by finding nothing -- which is
	// the exact failure mode it exists to catch, one level up.
	if claimsChecked < 4 {
		t.Errorf("only %d job-ceiling claims were found across .github/workflows, and there "+
			"were 4 when this guard was written. Either the comments were reworded past the "+
			"two spellings above, or the job/ceiling parsing has broken -- in both cases this "+
			"test is now green over nothing. Repoint it; do not delete it.", claimsChecked)
	}
}
