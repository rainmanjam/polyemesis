package testenv

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// EVERY BASE IMAGE THE RELEASE BUILDS FROM IS NAMED BY DIGEST.
//
// The three published images were built FROM floating tags (node:24-alpine,
// golang:1.27-alpine, alpine:3.24, nvidia/cuda:..., ubuntu:26.04), on purpose,
// so a rebuild picked up upstream security fixes. The cost was that two builds
// of one commit could start from different bytes, a release could not be
// rebuilt bit-for-bit, and whatever a registry served under that tag on the
// day of the release went into it unreviewed. Staging-readiness row 41.
//
// Pinning tag@sha256 keeps the fixes flowing through a reviewed PR instead:
// dependabot's docker ecosystem (.github/dependabot.yml, directory "/") reads
// the tag to know what to track and proposes a new digest when upstream moves.
// The TAG stays for that reason -- a bare @sha256 gives dependabot, and a
// reader, nothing to compare against.
//
// Scope: the Dockerfiles at the repository root, which are what release.yml
// publishes. web/Dockerfile and scripts/obs/Dockerfile are not published and
// dependabot is not pointed at their directories, so a digest there would
// only go stale.
func TestPublishedDockerfilesPinBaseImagesByDigest(t *testing.T) {
	root := repoRootFromTest(t)
	matches, err := filepath.Glob(filepath.Join(root, "Dockerfile*"))
	if err != nil {
		t.Fatal(err)
	}
	// POSITIVE CONTROL: release.yml builds three. Fewer means the glob moved.
	if len(matches) < 3 {
		t.Fatalf("found %d Dockerfile* at the repository root, expected at least 3 "+
			"(Dockerfile, Dockerfile.cuda, Dockerfile.vaapi)", len(matches))
	}

	fromRe := regexp.MustCompile(`(?i)^FROM\s+(?:--platform=\S+\s+)?(\S+)(?:\s+AS\s+(\S+))?\s*$`)
	pinned := regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]*:[A-Za-z0-9._-]+@sha256:[0-9a-f]{64}$`)
	froms := 0
	for _, path := range matches {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		name := filepath.Base(path)
		stages := map[string]bool{}
		for i, line := range strings.Split(string(b), "\n") {
			m := fromRe.FindStringSubmatch(strings.TrimSpace(line))
			if m == nil {
				continue
			}
			froms++
			ref := m[1]
			// FROM <earlier stage> and FROM scratch name no registry image.
			isStage := stages[strings.ToLower(ref)] || ref == "scratch"
			if m[2] != "" {
				stages[strings.ToLower(m[2])] = true
			}
			if isStage {
				continue
			}
			if !pinned.MatchString(ref) {
				t.Errorf("%s:%d: FROM %s is not pinned as name:tag@sha256:<digest>. A "+
					"floating tag lets the same commit build from different bytes; pin the "+
					"digest and let dependabot propose the next one.", name, i+1, ref)
			}
		}
	}
	if froms < 9 {
		t.Errorf("parsed only %d FROM lines across the root Dockerfiles, expected at least 9 "+
			"(three stages each); the FROM pattern has stopped matching", froms)
	}
}

// THE COMMENT ABOVE A PINNED FROM DESCRIBES THE PIN.
//
// Pinning Dockerfile.vaapi's runtime stage left the comment directly above it
// saying "Floating at 24.04, not pinned to a point release, so rebuilds collect
// security updates" -- over `FROM ubuntu:26.04@sha256:...`. Wrong twice: a
// digest collects nothing on rebuild, and the tag is 26.04. A reader deciding
// whether a rebuild picks up a CVE fix reads that comment, not the digest.
//
// Two checks on the comment block that runs contiguously into each FROM: it
// does not describe the image as floating, and any Ubuntu release it names is
// the one in the FROM reference.
func TestDockerfileFromCommentsMatchThePin(t *testing.T) {
	root := repoRootFromTest(t)
	matches, err := filepath.Glob(filepath.Join(root, "Dockerfile*"))
	if err != nil {
		t.Fatal(err)
	}
	fromRe := regexp.MustCompile(`(?i)^FROM\s+(?:--platform=\S+\s+)?(\S+)`)
	floating := regexp.MustCompile(`(?i)floating at|not pinned to|rebuilds collect`)
	ubuntuRel := regexp.MustCompile(`(?i)\bubuntu[: ]?(\d{2}\.\d{2})\b`)
	checked := 0
	for _, path := range matches {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		name := filepath.Base(path)
		lines := strings.Split(string(b), "\n")
		for i, line := range lines {
			m := fromRe.FindStringSubmatch(strings.TrimSpace(line))
			if m == nil || !strings.Contains(m[1], "@sha256:") {
				continue
			}
			checked++
			for j := i - 1; j >= 0 && strings.HasPrefix(strings.TrimSpace(lines[j]), "#"); j-- {
				c := lines[j]
				if floating.MatchString(c) {
					t.Errorf("%s:%d: the comment above the digest-pinned FROM on line %d says %q; "+
						"a digest does not float and a rebuild collects nothing. Describe the pin.",
						name, j+1, i+1, strings.TrimSpace(c))
				}
				for _, rel := range ubuntuRel.FindAllStringSubmatch(c, -1) {
					if !strings.Contains(m[1], rel[1]) {
						t.Errorf("%s:%d: the comment above the FROM on line %d names Ubuntu %s, "+
							"but the FROM is %s.", name, j+1, i+1, rel[1], m[1])
					}
				}
			}
		}
	}
	// POSITIVE CONTROL: three published Dockerfiles, three stages each.
	if checked < 9 {
		t.Errorf("checked only %d digest-pinned FROM lines, expected at least 9", checked)
	}
}
