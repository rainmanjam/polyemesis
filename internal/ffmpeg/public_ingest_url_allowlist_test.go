package ffmpeg

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

/* PublicIngestURL RENDERS A CREDENTIAL. WHO MAY CALL IT IS A SHORT LIST.
 *
 * It returns srt://host:port?...&passphrase=<cleartext> in SRT mode and the
 * operator's pull URL whole in pull mode. That is correct for the authenticated
 * settings page, which exists to show an operator their own ingest address, and
 * wrong for anything that writes to a log.
 *
 * It was wrong for two things. `ingest started` and `backup ingest started` both
 * called it, at Info, so every boot put the passphrase or the pull URL into
 * journalctl. Nothing caught it because nothing was watching WHO CALLS IT --
 * the rendering was correct, the caller was not.
 *
 * SO THIS IS AN ALLOWLIST, in the shape internal/alerts already uses for Redact:
 * TestRedactIsCalledOnlyFromTheAllowlist fails the build on a bare caller
 * outside a path-to-reason table, on the reasoning that "the declared secrets
 * are already gone; this is the best-effort pass over what is left" is a claim
 * only some callers are entitled to make. Same idea, opposite direction: the
 * full rendering is a disclosure only some callers are entitled to perform.
 *
 * ADDING A PATH HERE IS THE POINT, NOT A NUISANCE. If a new caller genuinely
 * needs the whole URL, write down why beside it. What must not happen is a
 * third log line acquiring one silently.
 */

// allowedPublicIngestURLCallers maps a path to the reason it may render the
// credential in full.
var allowedPublicIngestURLCallers = map[string]string{
	// The authenticated settings and source pages. An operator is being shown
	// their own ingest address so they can paste it into OBS; the passphrase is
	// the feature, not a leak. Both responses are principal-varying and
	// no-store — see internal/api/redact.go.
	"internal/api/handlers.go": "settings page shows the operator their own ingest URL",
	"internal/api/sources.go":  "source page shows the operator their own ingest URL",

	// The definition and its own tests.
	"internal/ffmpeg/build.go": "the definition",
}

func TestPublicIngestURLIsCalledOnlyFromTheAllowlist(t *testing.T) {
	root := repoRootForAllowlist(t)

	var offenders []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "node_modules", "dist", "vendor", "web", "ui":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if _, ok := allowedPublicIngestURLCallers[rel]; ok {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		for i, line := range strings.Split(string(b), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			if strings.Contains(line, ".PublicIngestURL(") {
				offenders = append(offenders, fmt.Sprintf("%s:%d", rel, i+1))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	if len(offenders) > 0 {
		t.Errorf("PublicIngestURL is called from %v, which is not on the allowlist.\n"+
			"It renders the SRT passphrase in the clear and the pull URL whole. If this "+
			"caller writes to a LOG, use IngestURLForLog instead — that is the bug this "+
			"test exists for, and it was live in `ingest started` and `backup ingest "+
			"started`. If the caller genuinely needs the full URL, add its path to "+
			"allowedPublicIngestURLCallers with the reason.", offenders)
	}
}

// repoRootForAllowlist walks up to the directory holding go.mod.
func repoRootForAllowlist(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's working directory")
		}
		dir = parent
	}
}
