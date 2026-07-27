package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// versionServer is the smallest Server the version endpoints touch: they read
// s.version and log, and nothing else.
func versionServer(version string) *Server {
	return &Server{version: version, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

// resetUpdateCache clears the process-wide cache so one case cannot see the
// previous one's result.
func resetUpdateCache(t *testing.T) {
	t.Helper()
	updateCache.Lock()
	updateCache.at = time.Time{}
	updateCache.latest, updateCache.url = "", ""
	updateCache.failed = false
	updateCache.Unlock()
}

// stubReleaseFeed points updateFeedURL at a local server and counts how many
// times it is actually reached, which is the only way to prove the endpoints
// stay off the network when they are supposed to.
func stubReleaseFeed(t *testing.T, h http.HandlerFunc) *atomic.Int64 {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		h(w, r)
	}))
	t.Cleanup(srv.Close)

	previous := updateFeedURL
	updateFeedURL = srv.URL
	t.Cleanup(func() { updateFeedURL = previous })
	return &hits
}

func releaseJSON(tag, url string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"tag_name":"`+tag+`","html_url":"`+url+`"}`)
	}
}

func decodeVersion(t *testing.T, w *httptest.ResponseRecorder) versionInfo {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", w.Code, w.Body.String())
	}
	var got versionInfo
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return got
}

func TestVersionEndpointNeverContactsTheReleaseFeed(t *testing.T) {
	resetUpdateCache(t)
	hits := stubReleaseFeed(t, releaseJSON("v9.9.9", "https://example.test/9"))

	s := versionServer("v1.0.0")
	w := httptest.NewRecorder()
	s.handleVersion(w, httptest.NewRequest(http.MethodGet, "/api/v1/version", nil))

	got := decodeVersion(t, w)
	if n := hits.Load(); n != 0 {
		t.Errorf("release feed reached %d times, want 0: reading the version must not phone home", n)
	}
	if got.Version != "v1.0.0" {
		t.Errorf("version = %q, want %q", got.Version, "v1.0.0")
	}
	if got.Latest != "" || got.CheckedAt != "" || got.UpdateAvailable {
		t.Errorf("uninvoked check leaked a result: %+v", got)
	}
}

func TestUpdateCheckReportsANewerRelease(t *testing.T) {
	resetUpdateCache(t)
	stubReleaseFeed(t, releaseJSON("v1.4.0", "https://example.test/v1.4.0"))

	s := versionServer("v1.2.3")
	w := httptest.NewRecorder()
	s.handleCheckUpdate(w, httptest.NewRequest(http.MethodPost, "/api/v1/version/check", nil))

	got := decodeVersion(t, w)
	if !got.UpdateAvailable || !got.Comparable {
		t.Errorf("updateAvailable=%v comparable=%v, want both true", got.UpdateAvailable, got.Comparable)
	}
	if got.Latest != "v1.4.0" {
		t.Errorf("latest = %q, want %q", got.Latest, "v1.4.0")
	}
	if got.ReleaseURL != "https://example.test/v1.4.0" {
		t.Errorf("releaseUrl = %q", got.ReleaseURL)
	}
	if got.CheckedAt == "" {
		t.Error("checkedAt is empty after a successful check")
	}
	if got.CheckFailed {
		t.Error("checkFailed is set after a successful check")
	}
}

func TestUpdateCheckResultIsReusedWithinTheTTL(t *testing.T) {
	resetUpdateCache(t)
	hits := stubReleaseFeed(t, releaseJSON("v2.0.0", "https://example.test/v2"))

	s := versionServer("v1.0.0")
	for range 3 {
		w := httptest.NewRecorder()
		s.handleCheckUpdate(w, httptest.NewRequest(http.MethodPost, "/api/v1/version/check", nil))
		decodeVersion(t, w)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("release feed reached %d times across 3 checks, want 1", n)
	}

	// And the plain read serves the same cached answer, still without asking.
	w := httptest.NewRecorder()
	s.handleVersion(w, httptest.NewRequest(http.MethodGet, "/api/v1/version", nil))
	if got := decodeVersion(t, w); got.Latest != "v2.0.0" {
		t.Errorf("latest = %q after a cached check, want %q", got.Latest, "v2.0.0")
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("release feed reached %d times, want 1", n)
	}
}

func TestUpdateCheckFailsQuietly(t *testing.T) {
	feeds := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"rate limited", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}},
		{"server error", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}},
		{"garbage body", func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, "<html>not json</html>")
		}},
		{"no tag in payload", releaseJSON("", "")},
	}

	for _, f := range feeds {
		t.Run(f.name, func(t *testing.T) {
			resetUpdateCache(t)
			stubReleaseFeed(t, f.handler)

			s := versionServer("v1.0.0")
			w := httptest.NewRecorder()
			s.handleCheckUpdate(w, httptest.NewRequest(http.MethodPost, "/api/v1/version/check", nil))

			// 200 with checkFailed, never a 5xx: an optional convenience must
			// not surface as a server error in the console.
			got := decodeVersion(t, w)
			if !got.CheckFailed {
				t.Error("checkFailed is not set after an unusable release feed")
			}
			if got.Latest != "" || got.UpdateAvailable {
				t.Errorf("failed check invented a result: %+v", got)
			}
			if got.Version != "v1.0.0" {
				t.Errorf("version = %q, want %q even when the check fails", got.Version, "v1.0.0")
			}
		})
	}
}

func TestSemverComparisonDecidesUpdateAvailability(t *testing.T) {
	cases := []struct {
		name           string
		latest         string
		current        string
		wantNewer      bool
		wantComparable bool
	}{
		{"major bump", "v2.0.0", "v1.9.9", true, true},
		{"minor bump", "v1.3.0", "v1.2.9", true, true},
		{"patch bump", "v1.2.4", "v1.2.3", true, true},
		{"identical", "v1.2.3", "v1.2.3", false, true},
		{"older tag than installed", "v1.2.3", "v1.3.0", false, true},
		{"tags without the v prefix", "1.2.4", "1.2.3", true, true},
		{"mixed v prefix", "v1.2.4", "1.2.3", true, true},
		{"double digit segments compare numerically", "v1.10.0", "v1.9.0", true, true},
		{"build metadata is ignored", "v1.2.3+deadbeef", "v1.2.3", false, true},
		{"release outranks its own prerelease", "v1.2.0", "v1.2.0-rc1", true, true},
		{"prerelease does not outrank the release", "v1.2.0-rc2", "v1.2.0", false, true},
		{"later prerelease of the same version", "v1.2.0-rc2", "v1.2.0-rc1", true, true},
		// Fail open: an unparseable version reports neither newer nor equal, so
		// the caller shows the tag it found instead of claiming a verdict.
		{"dev build cannot be compared", "v1.2.3", "dev", false, false},
		{"commit hash cannot be compared", "v1.2.3", "9f3a1c2", false, false},
		{"empty running version", "v1.2.3", "", false, false},
		{"non numeric tag", "release-candidate", "v1.2.3", false, false},
		{"two segment tag", "v1.2", "v1.2.3", false, false},
		{"four segment tag", "v1.2.3.4", "v1.2.3", false, false},
		{"empty tag", "", "v1.2.3", false, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			newer, comparable := newerThan(c.latest, c.current)
			if newer != c.wantNewer || comparable != c.wantComparable {
				t.Errorf("newerThan(%q, %q) = (%v, %v), want (%v, %v)",
					c.latest, c.current, newer, comparable, c.wantNewer, c.wantComparable)
			}
		})
	}
}

func TestUncomparableVersionsStillReportTheLatestTag(t *testing.T) {
	resetUpdateCache(t)
	stubReleaseFeed(t, releaseJSON("v1.4.0", "https://example.test/v1.4.0"))

	// A source build with no version stamped in. Refusing to mention the
	// release at all would leave this operator permanently unaware of it.
	s := versionServer("dev")
	w := httptest.NewRecorder()
	s.handleCheckUpdate(w, httptest.NewRequest(http.MethodPost, "/api/v1/version/check", nil))

	got := decodeVersion(t, w)
	if got.Latest != "v1.4.0" {
		t.Errorf("latest = %q, want %q even when the running version cannot be parsed", got.Latest, "v1.4.0")
	}
	if got.Comparable {
		t.Error("comparable is true for a version that is not a semantic version")
	}
	if got.UpdateAvailable {
		t.Error("updateAvailable is true from a comparison that could not be made")
	}
}
