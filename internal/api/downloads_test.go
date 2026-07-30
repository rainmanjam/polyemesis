package api

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/recording"
)

// The download routes hand a client-supplied string to os.Open. That makes them
// the highest-consequence untested code in this package: a name that escapes its
// directory turns an authenticated session into "read any file the server user
// can read", including the SQLite database and the secrets key beside it.
//
// The confinement is written and looks right. What was missing is anything that
// would notice if it stopped being.

// stemFile writes a file with a name this build recognises as a stem, and
// returns the name. The shape matters: resolveStem refuses anything that does
// not parse as a stem, so a test using an arbitrary name would exercise the
// filename check and never reach the confinement check behind it.
func writeStem(t *testing.T, dir, name, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return name
}

// canary is a secret-looking file planted OUTSIDE the directory a route is
// confined to. Every traversal below aims at it, and the assertion is simply
// that its contents never come back.
//
// Asserting on the status code instead does not work here and is worth
// recording: an unmatched /api path falls through to the SPA handler, which
// answers 200 with index.html. The first version of this test read that as "the
// file was SERVED" and reported a confinement failure that did not exist. What
// matters is not the number -- it is whether the bytes escaped.
const canaryBody = "CANARY-b7f3e2a1-must-never-be-served"

func plantCanary(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(canaryBody), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustNotLeak(t *testing.T, h http.Handler, sign func(*http.Request), method, path string) {
	t.Helper()
	r := jsonRequest(t, method, path, nil)
	sign(r)
	w := do(t, h, r)
	if strings.Contains(w.Body.String(), canaryBody) {
		t.Fatalf("%s %s served a file outside its directory (status %d)", method, path, w.Code)
	}
}

func TestStemDownloadServesAStemInsideTheStemsDirectory(t *testing.T) {
	h, _, sign := sourceServer(t)
	// Positive case first, so the refusals below cannot pass by refusing
	// everything -- which is the usual way a confinement test goes green while
	// the feature is broken.
	s := serverUnderTest(t, h)
	dir := s.eng().Recordings().StemsDir()
	name := writeStem(t, dir, "rec-20240115-143000-mic.flac", "STEMBYTES")

	body := send(t, h, sign, http.MethodGet,
		"/api/v1/recordings/stems/"+name+"/download", nil, http.StatusOK)
	if string(body) != "STEMBYTES" {
		t.Errorf("served %q, want the stem's contents", body)
	}
}

func TestStemDownloadRefusesAnythingOutsideTheStemsDirectory(t *testing.T) {
	h, _, sign := sourceServer(t)
	s := serverUnderTest(t, h)
	// One directory up from the stems directory, and one further still.
	plantCanary(t, s.eng().Recordings().Dir(), "secret.flac")
	plantCanary(t, s.cfg.DataDir, "secret.flac")

	for _, name := range []string{
		// Straight traversal, and the same escaped so it survives a router that
		// decodes before matching.
		"../secret.flac",
		"..%2Fsecret.flac",
		"%2E%2E%2Fsecret.flac",
		"../../secret.flac",
		"....//secret.flac",
		// Absolute paths, which filepath.Join would happily accept.
		"/etc/passwd",
		// A subdirectory: the stems directory is flat, and a name with a
		// separator in it is the first step of every traversal.
		"sub/rec-20240115-143000-mic.flac",
		// The same with a BACKSLASH, for the Windows reading of these names.
		//
		// Honest about what these do and do not prove: they do NOT pin
		// resolveStem's separator check. Removing that check entirely leaves
		// this test green, which was verified rather than assumed. The reason
		// is the ordering — ParseStemFilename runs behind it with an ANCHORED
		// regex, so a name containing any separator fails the shape check
		// whatever the separator rule says. The separator check here is
		// defence in depth that the shape check makes unreachable.
		//
		// They are kept because the PROPERTY is what this test is for: these
		// names must not serve bytes, and that must stay true however the two
		// checks are reordered or rewritten later. The place where the
		// separator rule is genuinely load-bearing, and genuinely pinned by a
		// mutation, is internal/recording and internal/media — neither has a
		// shape check in front of it.
		`sub\rec-20240115-143000-mic.flac`,
		`..\secret.flac`,
	} {
		t.Run(name, func(t *testing.T) {
			mustNotLeak(t, h, sign, http.MethodGet,
				"/api/v1/recordings/stems/"+url.PathEscape(name)+"/download")
		})
	}
}

func TestStemDownloadRefusesANameThatIsNotAStem(t *testing.T) {
	// The filename-shape check in front of the confinement. A name this build
	// never wrote is not a file this route will serve, whatever it points at.
	h, _, sign := sourceServer(t)
	s := serverUnderTest(t, h)
	// A real file, correctly inside the stems directory, with the wrong shape.
	writeStem(t, s.eng().Recordings().StemsDir(), "notes.txt", canaryBody)

	mustNotLeak(t, h, sign, http.MethodGet, "/api/v1/recordings/stems/notes.txt/download")
}

func TestStemDownloadIsA404WhenTheFileIsGone(t *testing.T) {
	// A well-formed name for a stem that retention already removed. This is the
	// ordinary case, and it must not be a 500: the recordings page links stems
	// it listed a moment ago, and a sweep in between is expected.
	h, _, sign := sourceServer(t)
	send(t, h, sign, http.MethodGet,
		"/api/v1/recordings/stems/rec-20240115-143000-mic.flac/download", nil, http.StatusNotFound)
}

func TestStemListIsEmptyRatherThanNullOnAFreshInstall(t *testing.T) {
	// null decodes to a nil array in the browser and throws on .map(). An empty
	// list is the difference between "no stems yet" and a blank page.
	h, _, sign := sourceServer(t)
	raw := send(t, h, sign, http.MethodGet, "/api/v1/recordings/stems", nil, http.StatusOK)
	if strings.Contains(string(raw), "null") {
		t.Errorf("stems list returned null: %s", raw)
	}
}

func TestClipDownloadRefusesAnythingOutsideTheClipDirectory(t *testing.T) {
	h, _, sign := sourceServer(t)
	s := serverUnderTest(t, h)
	plantCanary(t, s.cfg.DataDir, "secret.ts")

	for _, name := range []string{
		"../secret.ts",
		"..%2Fsecret.ts",
		"../../secret.ts",
		"/etc/passwd",
		"sub/clip.ts",
	} {
		t.Run(name, func(t *testing.T) {
			mustNotLeak(t, h, sign, http.MethodGet,
				"/api/v1/clips/"+url.PathEscape(name)+"/download")
		})
	}
}

func TestClipDeleteRefusesAnythingOutsideTheClipDirectory(t *testing.T) {
	// Delete is worse than download: a traversal here removes the file rather
	// than reading it, and there is nothing to put it back.
	h, _, sign := sourceServer(t)
	s := serverUnderTest(t, h)

	// A real file outside the clip directory, which must survive.
	outside := filepath.Join(s.cfg.DataDir, "do-not-delete.txt")
	if err := os.WriteFile(outside, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"../do-not-delete.txt", "..%2Fdo-not-delete.txt"} {
		r := jsonRequest(t, http.MethodDelete, "/api/v1/clips/"+url.PathEscape(name), nil)
		sign(r)
		do(t, h, r)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("a file outside the clip directory was deleted through the clips route: %v", err)
	}
}

func TestStemsDirIsInsideTheRecordingsDirectory(t *testing.T) {
	// The property every check above rests on. If the stems directory were ever
	// relocated outside the recordings tree, the confinement would still pass
	// its own tests while pointing somewhere new.
	h, _, _ := sourceServer(t)
	s := serverUnderTest(t, h)
	rec := s.eng().Recordings()
	stems, recdir := rec.StemsDir(), rec.Dir()
	if !strings.HasPrefix(stems, recdir) {
		t.Errorf("stems dir %q is not inside the recordings dir %q", stems, recdir)
	}
	if filepath.Base(stems) != recording.StemsSubdir {
		t.Errorf("stems dir base = %q, want %q", filepath.Base(stems), recording.StemsSubdir)
	}
}
