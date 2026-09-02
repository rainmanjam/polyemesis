package testenv

import (
	"bytes"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// NO TRACKED FILE MAY BE CHECKED OUT WITH CRLF.
//
// This is the device for a bug that has now been found five times, always the
// same shape and always in the most expensive place: a Windows checkout turns
// \n into \r\n, some test compares bytes or anchors a pattern on a line ending,
// and the failure appears on windows-latest and NOWHERE else -- so it arrives
// after the push, on a branch that was green everywhere the author looked.
//
// Worse than late, the failures accuse the wrong thing. The golden file
// reported "3200 of 3200 rows changed" and printed a blank diff, because \r
// walks the cursor back over the line just written. The contract test printed a
// got and a want that were, to the eye, identical. The markdown drift test
// reported that a heading "has moved or been renamed" about a document nobody
// had edited. The fifth, sonar_gate_test.go, reported that the `sonar` job had
// been renamed -- it had not; strings.Index(wf, "\n  sonar:\n") simply cannot
// match a file whose lines end in \r\n.
//
// Each of those was fixed by adding one more pattern to .gitattributes. Each
// fix was correct and none of them generalised, because the next guard reads
// the next file type. .gitattributes now defaults the whole repository to
// eol=lf, and this test is what keeps that true: deleting the default line
// fails here, on every platform, in the second it takes to run -- instead of on
// windows-latest, in five minutes, blaming an innocent file.
//
// Control rung. A new file type inherits the default rather than needing to be
// remembered, and the one place CRLF is genuinely wanted is named explicitly
// below rather than left to chance.
//
// It asks git to resolve the attribute rather than reading .gitattributes as
// text, because what matters is the value git actually applies -- a later line,
// a nested .gitattributes, or a pattern that does not match the way its author
// expected would all leave the file looking correct and behaving wrongly.

// crlfIsIntentional lists the paths allowed to check out with CRLF, by exact
// path. Deliberately not a glob: an exception that spreads by pattern is how
// the general rule gets hollowed out one file at a time.
var crlfIsIntentional = map[string]bool{
	"deploy/windows/install.ps1":   true,
	"deploy/windows/uninstall.ps1": true,
}

func TestNoTrackedFileChecksOutWithCRLF(t *testing.T) {
	root := repoRootFromTest(t)

	ls := exec.Command("git", "ls-files", "-z")
	ls.Dir = root
	lsOut, err := ls.Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	files := string(lsOut)
	paths := strings.Split(strings.TrimSuffix(files, "\x00"), "\x00")
	if len(paths) < 100 {
		// The check is worthless if it silently ran over nothing, and "no
		// violations found" is exactly what an empty list reports.
		t.Fatalf("git ls-files returned %d paths, which is too few for this "+
			"repository -- the check is measuring nothing. Fix the invocation; "+
			"do not delete the test.", len(paths))
	}

	check := exec.Command("git", "check-attr", "--stdin", "-z", "eol")
	check.Dir = root
	check.Stdin = strings.NewReader(files)
	var out, errb bytes.Buffer
	check.Stdout, check.Stderr = &out, &errb
	if err := check.Run(); err != nil {
		t.Fatalf("git check-attr: %v\n%s", err, errb.String())
	}

	// -z output is a flat NUL-separated stream of path, attribute, value.
	fields := strings.Split(strings.TrimSuffix(out.String(), "\x00"), "\x00")
	if len(fields)%3 != 0 {
		t.Fatalf("git check-attr returned %d fields, not a multiple of 3", len(fields))
	}

	var unset, wrongCRLF []string
	seen := 0
	for i := 0; i+2 < len(fields); i += 3 {
		path, value := fields[i], fields[i+2]
		seen++
		switch {
		case value == "lf":
			if crlfIsIntentional[path] {
				unset = append(unset, path+" (listed as an intentional CRLF file, but resolves to lf)")
			}
		case value == "crlf":
			if !crlfIsIntentional[path] {
				wrongCRLF = append(wrongCRLF, path)
			}
		default:
			// "unspecified" or "unset": git will apply the platform default,
			// which on Windows is CRLF. That is the hole this test exists for.
			unset = append(unset, path+" ("+value+")")
		}
	}
	if seen != len(paths) {
		t.Fatalf("checked %d files but %d are tracked", seen, len(paths))
	}

	if len(wrongCRLF) > 0 {
		t.Errorf("%d tracked file(s) are pinned to CRLF without being listed as "+
			"intentional:\n  %s\n\nIf a Windows consumer really needs CRLF, add the exact "+
			"path to crlfIsIntentional with the reason. Otherwise this file will reach a "+
			"byte-comparing test as \\r\\n and fail on windows-latest only.",
			len(wrongCRLF), strings.Join(truncate(wrongCRLF, 10), "\n  "))
	}
	if len(unset) > 0 {
		t.Errorf("%d tracked file(s) have no eol attribute, so a Windows checkout "+
			"decides for them:\n  %s\n\n.gitattributes is supposed to open with `* text=auto "+
			"eol=lf`, which covers every path. If that line was removed or narrowed, this is "+
			"the fifth time this bug will be found -- see the comment at the top of "+
			"eol_attribute_test.go for what the previous four cost.",
			len(unset), strings.Join(truncate(unset, 10), "\n  "))
	}
}

func truncate(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	return append(s[:n:n], "... and "+strconv.Itoa(len(s)-n)+" more")
}
