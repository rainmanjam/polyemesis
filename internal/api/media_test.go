package api

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/playlistmedia"
	"github.com/rainmanjam/polyemesis/internal/uploads"
)

// mediaServer returns a server whose data directory is a real temp dir, since
// every route here writes to it.
func mediaServer(t *testing.T) (http.Handler, string, func(*http.Request)) {
	t.Helper()
	dataDir := t.TempDir()
	_, h, _ := testServer(t, config.Config{DataDir: dataDir})
	return h, dataDir, login(t, h)
}

// multipartBody builds a multipart body with one file part.
func multipartBody(t *testing.T, field, filename, content string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile(field, filename)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := fw.Write([]byte(content)); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return &buf, mw.FormDataContentType()
}

func uploadRequest(t *testing.T, field, filename, content string) *http.Request {
	t.Helper()
	body, ctype := multipartBody(t, field, filename, content)
	r := httptest.NewRequest(http.MethodPost, "/api/v1/media", body)
	r.Header.Set("Content-Type", ctype)
	r.RemoteAddr = "203.0.113.5:44444"
	return r
}

func TestUploadStoresTheFileAndTagsItsOrigin(t *testing.T) {
	h, dataDir, auth := mediaServer(t)

	r := uploadRequest(t, "file", "My Show.mp4", "pretend media bytes")
	auth(r)
	w := do(t, h, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	var got uploads.File
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Origin != uploads.OriginUploaded {
		t.Fatalf("Origin = %q, want %q", got.Origin, uploads.OriginUploaded)
	}
	if got.Bytes != int64(len("pretend media bytes")) {
		t.Fatalf("Bytes = %d", got.Bytes)
	}
	// The file must actually be on disk under the uploads directory, not
	// merely described in the response.
	full := filepath.Join(dataDir, uploads.Dir, got.Name)
	if _, err := os.Stat(full); err != nil {
		t.Fatalf("uploaded file is not on disk: %v", err)
	}
}

// The client's filename must not survive as a path. This is the whole reason
// the store generates names, and the handler is where a hostile one arrives.
func TestUploadDiscardsAHostileFilename(t *testing.T) {
	h, dataDir, auth := mediaServer(t)

	r := uploadRequest(t, "file", "../../../../etc/passwd", "x")
	auth(r)
	w := do(t, h, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var got uploads.File
	json.Unmarshal(w.Body.Bytes(), &got)
	if strings.ContainsAny(got.Name, `/\`) {
		t.Fatalf("stored name contains a separator: %q", got.Name)
	}
	// Nothing may exist outside the uploads directory.
	if _, err := os.Stat(filepath.Join(dataDir, "etc", "passwd")); err == nil {
		t.Fatal("upload escaped the uploads directory")
	}
	entries, err := os.ReadDir(filepath.Join(dataDir, uploads.Dir))
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected exactly one file in uploads/, got %v (err %v)", entries, err)
	}
}

func TestUploadRejectsABodyWithNoFilePart(t *testing.T) {
	h, _, auth := mediaServer(t)

	// A well-formed multipart body whose part has the wrong field name.
	r := uploadRequest(t, "notthefile", "show.mp4", "data")
	auth(r)
	w := do(t, h, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
}

func TestUploadRejectsANonMultipartBody(t *testing.T) {
	h, _, auth := mediaServer(t)

	r := httptest.NewRequest(http.MethodPost, "/api/v1/media", strings.NewReader(`{"not":"multipart"}`))
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = "203.0.113.5:44444"
	auth(r)
	w := do(t, h, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestUploadRejectsAnEmptyFile(t *testing.T) {
	h, dataDir, auth := mediaServer(t)

	r := uploadRequest(t, "file", "empty.mp4", "")
	auth(r)
	w := do(t, h, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	// And it must leave nothing behind: a zero-byte file in the listing would
	// be selectable as a source that cannot play.
	entries, _ := os.ReadDir(filepath.Join(dataDir, uploads.Dir))
	if len(entries) != 0 {
		t.Fatalf("empty upload left %d files behind", len(entries))
	}
}

// Upload is a mutation, so it sits behind session + CSRF like every other one.
// An unauthenticated caller must not be able to write bytes to the disk.
func TestUploadRequiresASession(t *testing.T) {
	h, dataDir, _ := mediaServer(t)

	r := uploadRequest(t, "file", "show.mp4", "data")
	// deliberately not authenticated
	w := do(t, h, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	entries, _ := os.ReadDir(filepath.Join(dataDir, uploads.Dir))
	if len(entries) != 0 {
		t.Fatalf("an unauthenticated upload wrote %d files", len(entries))
	}
}

// CSRF specifically: a session cookie alone is not enough for a mutation, or a
// cross-site form post could upload on the operator's behalf.
func TestUploadRequiresCSRF(t *testing.T) {
	h, dataDir, auth := mediaServer(t)

	r := uploadRequest(t, "file", "show.mp4", "data")
	auth(r)
	r.Header.Del("X-CSRF-Token")
	w := do(t, h, r)
	if w.Code == http.StatusCreated {
		t.Fatal("upload succeeded without a CSRF token")
	}
	entries, _ := os.ReadDir(filepath.Join(dataDir, uploads.Dir))
	if len(entries) != 0 {
		t.Fatalf("an upload without CSRF wrote %d files", len(entries))
	}
}

func TestListMediaReturnsWhatWasUploaded(t *testing.T) {
	h, _, auth := mediaServer(t)

	for _, name := range []string{"one.mp4", "two.mkv"} {
		r := uploadRequest(t, "file", name, "bytes for "+name)
		auth(r)
		if w := do(t, h, r); w.Code != http.StatusCreated {
			t.Fatalf("upload %s: status %d", name, w.Code)
		}
	}

	r := httptest.NewRequest(http.MethodGet, "/api/v1/media", nil)
	r.RemoteAddr = "203.0.113.5:44444"
	auth(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var list []uploads.File
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("got %d files, want 2", len(list))
	}
	for _, f := range list {
		if f.Origin != uploads.OriginUploaded {
			t.Fatalf("Origin = %q on %q", f.Origin, f.Name)
		}
		if f.PullURL == "" {
			t.Fatalf("no pull URL on %q", f.Name)
		}
	}
}

func TestDeleteMediaRemovesTheFile(t *testing.T) {
	h, dataDir, auth := mediaServer(t)

	r := uploadRequest(t, "file", "gone.mp4", "data")
	auth(r)
	w := do(t, h, r)
	var got uploads.File
	json.Unmarshal(w.Body.Bytes(), &got)

	del := httptest.NewRequest(http.MethodDelete, "/api/v1/media/"+got.Name, nil)
	del.RemoteAddr = "203.0.113.5:44444"
	auth(del)
	if w := do(t, h, del); w.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, body = %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dataDir, uploads.Dir, got.Name)); !os.IsNotExist(err) {
		t.Fatalf("file still present after delete: %v", err)
	}
}

// The body is checked as well as the status. A 404 alone is also what the SPA
// fallback answers for a path with no route left, whenever the UI has not been
// built -- which is how CI's Go job runs. See mustJSONError in renditions_test.go.
func TestDeleteMediaOnAMissingNameIs404(t *testing.T) {
	h, _, auth := mediaServer(t)

	r := httptest.NewRequest(http.MethodDelete, "/api/v1/media/never-existed.mp4", nil)
	r.RemoteAddr = "203.0.113.5:44444"
	auth(r)
	w := do(t, h, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	var got apiError
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.Error == "" {
		t.Fatalf("404 carried no JSON error (%v); the SPA fallback answered "+
			"instead of the delete route: %.80s", err, w.Body.String())
	}
}

// A traversal in the delete path must not reach outside the uploads directory.
// chi will not route a literal slash into {name}, so the reachable hostile
// forms are the encoded ones and the ones that survive URL parsing.
// TestDeletingAGlobNameRemovesNobodyElsesDerivative is a REGRESSION test for a
// destructive defect, and it exists because the traversal test below could never
// have caught it.
//
// The delete handler used to expand playlistmedia.DerivativeGlob through
// filepath.Glob, and the name it built that pattern from is a URL path segment.
// ValidUploadName rejects separators and control characters, but `*`, `?` and
// `[` are legal in a filename and it says nothing about them -- so
// `DELETE /api/v1/media/%2A` produced `<dataDir>/playlist-media/*.v*.ts` and
// removed EVERY DERIVATIVE IN THE INSTALL. The name was validated afterwards, by
// store.Delete, which is far too late to matter: the files were already gone.
//
// TestDeleteMediaRefusesTraversal only ever checks that a file OUTSIDE the
// uploads directory survives. It never looks in playlist-media/, so every
// derivative in the install could be deleted with that test still green. A guard
// that watches one directory cannot speak for another.
//
// The mutation: put filepath.Glob(DerivativeGlob(...)) back and this fails.
func TestDeletingAGlobNameRemovesNobodyElsesDerivative(t *testing.T) {
	h, dataDir, auth := mediaServer(t)

	// Somebody else's derivative, of an upload this request does not name.
	other := playlistmedia.DerivativePath(dataDir, "someone-elses.mp4")
	if err := os.MkdirAll(filepath.Dir(other), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("not yours"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Every metacharacter that means something to filepath.Match. `[a-z]*` is
	// here for a second reason: an unterminated class returns ErrBadPattern,
	// which the old code surfaced as a 500 on what is really a bad request.
	// THE SURVIVOR IS THE ASSERTION, not the status, and the status is
	// deliberately not pinned because it legitimately differs by platform.
	//
	// On POSIX these names are merely absent, so the answer is 404. On Windows
	// `*` and `?` are not permissible in a filename at all, so the remove fails
	// with a syntax error and the handler answers 400 -- which is the BETTER
	// answer: the request was malformed, not merely unsatisfiable. Asserting 404
	// here failed on windows-latest for that reason, and the product was right
	// both times.
	//
	// What must hold everywhere is that somebody else's derivative is still
	// there. The old code deleted the derivatives FIRST and only then asked
	// store.Delete about the name, so it answered "no such upload" having
	// already destroyed them -- a status assertion would have passed while the
	// install was being emptied.
	for _, name := range []string{"*", "?", "[a-z]*", "*.mp4"} {
		r := jsonRequest(t, http.MethodDelete, "/api/v1/media/"+url.PathEscape(name), nil)
		auth(r)
		if code := do(t, h, r).Code; code != http.StatusNotFound && code != http.StatusBadRequest {
			t.Errorf("deleting %q: status %d, want 404 (absent) or 400 (malformed on this platform)",
				name, code)
		}
		if _, err := os.Stat(other); err != nil {
			t.Fatalf("deleting %q removed a derivative belonging to a different upload: %v", name, err)
		}
	}
}

func TestDeleteMediaRefusesTraversal(t *testing.T) {
	h, dataDir, auth := mediaServer(t)

	victim := filepath.Join(dataDir, "secret.txt")
	if err := os.WriteFile(victim, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"..%2Fsecret.txt", "%2e%2e%2fsecret.txt", `..\secret.txt`} {
		r := httptest.NewRequest(http.MethodDelete, "/api/v1/media/"+name, nil)
		r.RemoteAddr = "203.0.113.5:44444"
		auth(r)
		w := do(t, h, r)
		if w.Code == http.StatusNoContent {
			t.Fatalf("delete %q reported success", name)
		}
		if _, err := os.Stat(victim); err != nil {
			t.Fatalf("delete %q removed a file outside uploads/: %v", name, err)
		}
	}
}

// The in-use guard B1 deferred. Defensible now in a way it was not then: B1's
// lockout came from punishing an operator for state they could not edit, and
// B2 gives them the control -- see handleDeleteMedia.
//
// A playlist-and-media fixture is needed here rather than plain mediaServer:
// PUT /settings already reconciles unconditionally (handlePutSettings calls
// s.mgr.Reconcile with no nil guard), so saving a playlist needs a server
// with an engine wired, which is what sourceServer/serverUnderTest build.
//
// The mutation: delete the uploadIsReferenced check and this returns 204.
func TestDeletingAnUploadAPlaylistNamesIsRefused(t *testing.T) {
	h, sign, srv, _ := playlistJobServer(t)
	seedUpload(t, srv, "used.ts")

	savePlaylist(t, h, sign, []string{"used.ts"}, http.StatusOK)

	del := httptest.NewRequest(http.MethodDelete, "/api/v1/media/used.ts", nil)
	del.RemoteAddr = "203.0.113.5:44444"
	sign(del)
	w := do(t, h, del)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "item 0") {
		t.Errorf("the refusal does not name the referencing item's index: %s", w.Body.String())
	}
	// Refused means refused: the file must still be there.
	if _, err := os.Stat(filepath.Join(srv.cfg.DataDir, uploads.Dir, "used.ts")); err != nil {
		t.Fatalf("a refused delete removed the upload anyway: %v", err)
	}
}

// A permitted deletion removes EVERY derivative version, not just the current
// profile's: a version bump can leave more than one on disk, and deleting the
// upload while orphaning them is the leak B1 carried. See
// playlistmedia.DerivativeVersions.
//
// The mutation: remove only DerivativePath's exact name and the v1 file
// remains.
func TestAPermittedDeletionRemovesEveryDerivativeVersion(t *testing.T) {
	h, dataDir, auth := mediaServer(t)

	r := uploadRequest(t, "file", "unused.ts", "data")
	auth(r)
	w := do(t, h, r)
	var got uploads.File
	json.Unmarshal(w.Body.Bytes(), &got)

	derivDir := playlistmedia.DerivativeDir(dataDir)
	if err := os.MkdirAll(derivDir, 0o755); err != nil {
		t.Fatalf("mkdir derivative dir: %v", err)
	}
	v1 := filepath.Join(derivDir, got.Name+".v1.ts")
	v2 := filepath.Join(derivDir, got.Name+".v2.ts")
	for _, p := range []string{v1, v2} {
		if err := os.WriteFile(p, []byte("derivative"), 0o644); err != nil {
			t.Fatalf("seed %s: %v", p, err)
		}
	}

	del := httptest.NewRequest(http.MethodDelete, "/api/v1/media/"+got.Name, nil)
	del.RemoteAddr = "203.0.113.5:44444"
	auth(del)
	if w := do(t, h, del); w.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, body = %s", w.Code, w.Body.String())
	}

	for _, p := range []string{v1, v2} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("derivative %s still present after delete: %v", p, err)
		}
	}
}

// The media writes are session-only, and this is the test that says so by
// executing the router rather than by reading the comment above the routes.
//
// SECURITY.md, docs/API.md and the header comment in media.go all claimed an
// API token could not upload, while the routes carried requireAuth and
// requireCSRF and nothing else -- and requireCSRF waves a token principal
// through by design, because nothing attaches an Authorization header on its
// own. The claim was therefore enforced by no code at all, and a token-only
// POST stored a file. Reading the disk rather than only the status is the
// point: a 403 with the bytes already written would still be the bug.
func TestAPITokenCannotUploadOrDeleteMedia(t *testing.T) {
	h, dataDir, sign := mediaServer(t)
	plaintext := createToken(t, h, sign, "ci runner")

	upload := uploadRequest(t, "file", "smuggled.mp4", "pretend media bytes")
	upload.Header.Set("Authorization", "Bearer "+plaintext)
	if w := do(t, h, upload); w.Code != http.StatusForbidden {
		t.Errorf("token upload status = %d, want %d, body %s",
			w.Code, http.StatusForbidden, w.Body.String())
	}

	// Nothing on disk. The uploads directory is created lazily, so its absence
	// is as good an answer as its emptiness.
	entries, err := os.ReadDir(filepath.Join(dataDir, uploads.Dir))
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read uploads dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("token upload left %d file(s) on disk: %v", len(entries), entries)
	}

	// A file a session put there, so the delete probe addresses something real
	// and cannot pass merely by being a 404.
	seed := uploadRequest(t, "file", "real.mp4", "data")
	sign(seed)
	sw := do(t, h, seed)
	if sw.Code != http.StatusCreated {
		t.Fatalf("seed upload: status %d, body %s", sw.Code, sw.Body.String())
	}
	var seeded uploads.File
	if err := json.Unmarshal(sw.Body.Bytes(), &seeded); err != nil {
		t.Fatalf("decode seed: %v", err)
	}

	del := httptest.NewRequest(http.MethodDelete, "/api/v1/media/"+seeded.Name, nil)
	del.RemoteAddr = "203.0.113.5:44444"
	del.Header.Set("Authorization", "Bearer "+plaintext)
	if w := do(t, h, del); w.Code != http.StatusForbidden {
		t.Errorf("token delete status = %d, want %d, body %s",
			w.Code, http.StatusForbidden, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dataDir, uploads.Dir, seeded.Name)); err != nil {
		t.Errorf("token delete removed the file anyway: %v", err)
	}
}

// The read stays reachable. A token is for automation, and listing what is
// already stored is exactly the kind of thing automation is for; narrowing the
// docs to "list, not write" is only honest if the list genuinely still works.
func TestAPITokenCanStillListMedia(t *testing.T) {
	h, _, sign := mediaServer(t)
	plaintext := createToken(t, h, sign, "ci runner")

	r := httptest.NewRequest(http.MethodGet, "/api/v1/media", nil)
	r.RemoteAddr = "203.0.113.5:44444"
	r.Header.Set("Authorization", "Bearer "+plaintext)
	if w := do(t, h, r); w.Code != http.StatusOK {
		t.Fatalf("token list status = %d, want 200, body %s", w.Code, w.Body.String())
	}
}
