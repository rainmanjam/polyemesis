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
