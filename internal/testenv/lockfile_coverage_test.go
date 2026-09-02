package testenv

// A LOCKFILE NOBODY WATCHES IS A DEPENDENCY TREE NOBODY HAS READ.
//
// This repository has two package-lock.json files. ui/ was audited by
// .github/workflows/security.yml and tracked by .github/dependabot.yml. web/ --
// the documentation site, 342 KB of resolved dependency tree -- was in neither:
// dependabot.yml listed `/` and `/ui`, and the npm-audit job hardcoded
// `working-directory: ui`. Nothing would have reported a high-severity advisory
// against it, and nothing would have opened a PR to move it off one.
//
// The interesting part is HOW it happened, because it is the shape this guard
// is aimed at rather than the one instance. web/ was added second. Neither of
// the two files that had to change said anywhere that they were meant to cover
// every lockfile; each just named a directory, correctly, at the time it was
// written. There was no moment at which anybody decided web/ should go
// unaudited, and no artefact that would have gone red when it did.
//
// Control rung: a third lockfile cannot land without this failing. That is
// available here and was not available to the two config files themselves --
// neither YAML dialect can enumerate a directory.

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEveryLockfileIsAuditedAndTracked(t *testing.T) {
	root := repoRootFromTest(t)

	var dirs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", ".git", "dist", "data":
				return fs.SkipDir
			}
			return nil
		}
		if d.Name() != "package-lock.json" {
			return nil
		}
		rel, rerr := filepath.Rel(root, filepath.Dir(path))
		if rerr != nil {
			return rerr
		}
		dirs = append(dirs, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walking the tree: %v", err)
	}

	// Liveness. If the walk stops finding lockfiles, this test passes over an
	// empty set -- which is the same failure it exists to catch.
	if len(dirs) < 2 {
		t.Fatalf("found %d package-lock.json files (%v); there were 2 when this guard was "+
			"written. Either the walk has broken or a project was removed. Repoint this "+
			"rather than deleting it: green over nothing is what it is here to prevent.",
			len(dirs), dirs)
	}

	dependabot := mustReadRepoFile(t, root, ".github", "dependabot.yml")
	security := mustReadRepoFile(t, root, ".github", "workflows", "security.yml")

	for _, dir := range dirs {
		// dependabot.yml addresses a project by `directory: "/ui"`.
		if !strings.Contains(dependabot, `directory: "/`+dir+`"`) {
			t.Errorf("%s/package-lock.json is not tracked by .github/dependabot.yml.\n\n"+
				"Nothing will open a pull request when one of its dependencies publishes a "+
				"security release, and nobody will find out by any other route -- there is no "+
				"human step in this repository that reads a lockfile. Add:\n\n"+
				"    - package-ecosystem: npm\n      directory: \"/%s\"\n"+
				"      schedule: { interval: weekly, day: monday }\n", dir, dir)
		}
		// security.yml's npm-audit job addresses one by matrix entry.
		if !strings.Contains(security, "project: [") || !auditMatrixCovers(security, dir) {
			t.Errorf("%s/package-lock.json is not audited by the npm-audit job in "+
				".github/workflows/security.yml.\n\n"+
				"That job runs `npm audit --omit=dev --audit-level=high` over a matrix of "+
				"project directories, and a directory missing from the matrix is a dependency "+
				"tree no advisory check ever reads. Add %q to `matrix.project`.\n\n"+
				"web/ was in exactly this position: added second, named in neither file, and "+
				"audited by nothing for as long as it existed.", dir, dir)
		}
	}
}

// auditMatrixCovers reports whether security.yml's npm-audit matrix lists dir.
// Read out of the workflow rather than transcribed, so a rename of the job or
// the matrix key surfaces as a failure here rather than as silence.
func auditMatrixCovers(security, dir string) bool {
	i := strings.Index(security, "project: [")
	if i < 0 {
		return false
	}
	rest := security[i+len("project: ["):]
	j := strings.Index(rest, "]")
	if j < 0 {
		return false
	}
	for _, entry := range strings.Split(rest[:j], ",") {
		if strings.TrimSpace(entry) == dir {
			return true
		}
	}
	return false
}

func mustReadRepoFile(t *testing.T, root string, parts ...string) string {
	t.Helper()
	path := filepath.Join(append([]string{root}, parts...)...)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}
