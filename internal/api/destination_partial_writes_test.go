package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// NOTHING THAT LANDS WHILE A HANDLER IS AWAY AT A PLATFORM IS UNDONE BY IT.
//
// Refresh stream key and the save that first turns on Facebook's backup ingest
// read a destination, make a platform call, and used to write the WHOLE row
// back afterwards. Anything that landed during the call -- a stop, a start, a
// rename, a routing change, an expert save -- was silently reverted, and a
// reverted stop is a destination the reconcile then starts again. These drive
// the real handlers against the platform stub and land the edit INSIDE the
// stub's answer, which is where it lands in production.

// sendJSON is jsonRequest + do without a *testing.T, for an edit made from
// inside the stub's handler goroutine, where t.Fatal must not be called.
func sendJSON(h http.Handler, sign func(*http.Request), method, path string, body any) error {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return err
		}
	}
	r := httptest.NewRequest(method, path, &buf)
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = "203.0.113.5:44444"
	sign(r)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code >= 300 {
		return fmt.Errorf("%s %s: status %d, body %s", method, path, w.Code, w.Body.String())
	}
	return nil
}

func destinationPath(id int64, suffix string) string {
	return "/api/v1/destinations/" + strconv.FormatInt(id, 10) + suffix
}

// refreshFixture is an engine-backed server (refresh-key carries
// requireSource) whose providers all point at the stub, with a connected
// Facebook account, its developer credentials, and one destination linked to
// it.
type refreshFixture struct {
	h      http.Handler
	store  *db.DB
	sign   func(*http.Request)
	stub   *platformStub
	dest   *db.Destination
	acctID int64
	s      *Server
}

func newRefreshFixture(t *testing.T, enabled bool) refreshFixture {
	t.Helper()
	stub := newPlatformStub(t)
	s, h, store, sign := engineServer(t, defaultTools(), Options{Providers: stub.set()})
	acctID := connectAccount(t, store, s.box, db.PlatformFacebook, "ada")
	if err := store.PutPlatformCreds(s.box, db.PlatformFacebook, "cid", "topsecret"); err != nil {
		t.Fatalf("creds: %v", err)
	}
	dest, err := store.CreateDestination(&db.Destination{
		Name: "Facebook", Kind: db.DestRTMP, Platform: db.PlatformFacebook,
		URL: "rtmps://ingest.example/app", StreamKey: "sk-live-old", AccountID: &acctID,
		Enabled: enabled,
	})
	if err != nil {
		t.Fatalf("create destination: %v", err)
	}
	return refreshFixture{h: h, store: store, sign: sign, stub: stub, dest: dest, acctID: acctID, s: s}
}

// landDuring runs edit once, inside the first stub request match accepts, and
// returns a function that reports what it returned. It fails the test if the
// edit never ran, which is the control: a pass with no edit landed asserts
// nothing.
func landDuring(t *testing.T, stub *platformStub, match func(stubCall) bool, edit func() error) func() {
	t.Helper()
	var once sync.Once
	landed := make(chan error, 1)
	stub.setOnCall(func(c stubCall) {
		if match(c) {
			once.Do(func() { landed <- edit() })
		}
	})
	return func() {
		t.Helper()
		select {
		case err := <-landed:
			if err != nil {
				t.Fatalf("the concurrent edit itself failed: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the concurrent edit never ran, so this test asserts nothing")
		}
	}
}

func isFacebookCreate(c stubCall) bool {
	return c.Method == http.MethodPost && c.Path == "/me/live_videos"
}

// A stop, a start, a rename and a routing change made while Refresh stream key
// waits for the platform all survive the refresh, and the refresh still stores
// the key the platform answered.
//
// Mutation: in handleRefreshKey, replace the UpdateDestinationIngest call with
// the old write -- set dest.URL, dest.StreamKey, dest.Kind and the backup pair
// on the snapshot read before the platform call, then
// `updated, err := s.store.UpdateDestination(dest)`. Observed red on all four
// cases: the destination stopped during the refresh is enabled again, the one
// started is disabled, the rename and the routing change are gone.
func TestARefreshedKeyDoesNotUndoWhatLandedWhileThePlatformAnswered(t *testing.T) {
	halfGain := routing.DefaultProfile()
	halfGain.Tracks[0].Gain = 0.5

	cases := []struct {
		name    string
		enabled bool
		edit    func(f refreshFixture) error
		check   func(got *db.Destination) string
	}{{
		// Through the store's one start/stop write: every stop route arrives
		// at it, and the HTTP stop of a Facebook destination would add its own
		// platform calls to the stub's log.
		name: "stop", enabled: true,
		edit: func(f refreshFixture) error { return f.store.SetDestinationEnabled(f.dest.ID, false) },
		check: func(got *db.Destination) string {
			if got.Enabled {
				return "the destination stopped during the refresh is enabled again, so the reconcile starts it"
			}
			return ""
		},
	}, {
		name: "start", enabled: false,
		edit: func(f refreshFixture) error { return f.store.SetDestinationEnabled(f.dest.ID, true) },
		check: func(got *db.Destination) string {
			if !got.Enabled {
				return "the destination started during the refresh is disabled again"
			}
			return ""
		},
	}, {
		name: "rename",
		edit: func(f refreshFixture) error {
			return sendJSON(f.h, f.sign, http.MethodPut, destinationPath(f.dest.ID, ""),
				map[string]any{"name": "renamed during the refresh"})
		},
		check: func(got *db.Destination) string {
			if got.Name != "renamed during the refresh" {
				return fmt.Sprintf("Name = %q: the rename made during the refresh was reverted", got.Name)
			}
			return ""
		},
	}, {
		name: "routing change",
		edit: func(f refreshFixture) error {
			return sendJSON(f.h, f.sign, http.MethodPut, destinationPath(f.dest.ID, ""),
				map[string]any{"profile": halfGain})
		},
		check: func(got *db.Destination) string {
			if g := got.Profile.Tracks[0].Gain; g != 0.5 {
				return fmt.Sprintf("track 1 gain = %v: the routing change made during the refresh was reverted", g)
			}
			return ""
		},
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRefreshFixture(t, tc.enabled)
			ran := landDuring(t, f.stub, isFacebookCreate, func() error { return tc.edit(f) })

			r := jsonRequest(t, http.MethodPost, destinationPath(f.dest.ID, "/refresh-key"), nil)
			f.sign(r)
			if w := do(t, f.h, r); w.Code != http.StatusOK {
				t.Fatalf("refresh-key: status %d, body %s", w.Code, w.Body.String())
			}
			ran()

			got, err := f.store.GetDestination(f.dest.ID)
			if err != nil {
				t.Fatalf("GetDestination: %v", err)
			}
			if msg := tc.check(got); msg != "" {
				t.Error(msg)
			}
			// The control: the refresh did write. Without it a handler that
			// stored nothing at all would pass every check above.
			if got.StreamKey != "sk-from-the-broadcast" || !strings.Contains(got.URL, "live.example") {
				t.Errorf("ingest = %q / %q, want the key and URL the platform answered", got.URL, got.StreamKey)
			}
			if got.BackupStreamKey != "backup-key" {
				t.Errorf("BackupStreamKey = %q, want the backup the platform answered", got.BackupStreamKey)
			}
		})
	}
}

// A key fetched with one account is not stored on a destination that was
// linked to another while the platform answered: it would publish to a
// channel the operator has just moved this destination away from. Nothing is
// written and the answer says why.
//
// Mutation: delete the account check in handleRefreshKey's callback. Observed
// red: 200, and the first account's key stored on a destination now linked to
// the second.
func TestARefreshedKeyIsNotStoredWhenTheAccountChangedDuringTheFetch(t *testing.T) {
	f := newRefreshFixture(t, false)
	other := connectAccount(t, f.store, f.s.box, db.PlatformFacebook, "grace")
	ran := landDuring(t, f.stub, isFacebookCreate, func() error {
		return sendJSON(f.h, f.sign, http.MethodPut, destinationPath(f.dest.ID, ""),
			map[string]any{"accountId": other})
	})

	r := jsonRequest(t, http.MethodPost, destinationPath(f.dest.ID, "/refresh-key"), nil)
	f.sign(r)
	w := do(t, f.h, r)
	ran()
	if w.Code != http.StatusConflict {
		t.Fatalf("refresh-key: status %d, want 409 (body %s)", w.Code, w.Body.String())
	}
	got, err := f.store.GetDestination(f.dest.ID)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}
	if got.AccountID == nil || *got.AccountID != other {
		t.Errorf("AccountID = %v, want the account linked during the fetch (%d)", got.AccountID, other)
	}
	if got.StreamKey != "sk-live-old" {
		t.Errorf("StreamKey = %q: a key fetched with the old account was stored anyway", got.StreamKey)
	}
}

// The save that first turns on a Facebook backup ingest asks Graph for it.
// A stop and an expert save that land while Graph answers survive the save,
// and the backup Facebook added is still stored.
//
// The edits are a stop and an expert save because those are what can land
// there: the PUT holds trackGainMu, so a rename, a routing change or a level
// change waits behind it rather than landing inside it.
//
// Mutation: in handleUpdateDestination, move the backup call back in front of
// the write -- `if w := s.fillFacebookBackupIngest(r.Context(), existing); w
// != "" { warnings = append(warnings, w) }` before `before :=`, and drop the
// addBackupIngestAfterSave line. Observed red: the destination stopped during
// the call is enabled again and the expert arguments are gone.
func TestTurningOnABackupDoesNotUndoWhatLandedWhileFacebookAnswered(t *testing.T) {
	stub := newPlatformStub(t)
	s, h, store, sign := engineServer(t, defaultTools(), Options{Providers: stub.set()})
	acctID := linkFacebookAccount(t, s, store)
	// A FETCHED key, which carries the live-video id: what lets polyemesis add
	// a feed to the broadcast rather than create a new one.
	dest, err := store.CreateDestination(&db.Destination{
		Name: "fb", Kind: db.DestRTMP, Platform: db.PlatformFacebook,
		URL: "rtmps://live-api-s.facebook.com:443/rtmp", StreamKey: "778899",
		Enabled: true, AccountID: &acctID,
	})
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}

	ran := landDuring(t, stub, func(c stubCall) bool {
		return c.Method == http.MethodPost && strings.HasSuffix(c.Path, "/input_streams")
	}, func() error {
		if err := store.SetDestinationEnabled(dest.ID, false); err != nil {
			return err
		}
		// The expert route takes no lock the PUT holds, so it really can land
		// here.
		return sendJSON(h, sign, http.MethodPut, destinationPath(dest.ID, "/expert"),
			map[string]any{"outputArgs": "-metadata title=x", "confirm": true})
	})

	r := jsonRequest(t, http.MethodPut, destinationPath(dest.ID, ""),
		map[string]any{"backupIngestWanted": true})
	sign(r)
	w := do(t, h, r)
	ran()
	if w.Code != http.StatusOK {
		t.Fatalf("PUT: status %d, body %s", w.Code, w.Body.String())
	}

	got, err := store.GetDestination(dest.ID)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}
	if got.Enabled {
		t.Error("the destination stopped while Facebook answered is enabled again, so the reconcile starts it")
	}
	if got.ExtraOutputArgs != "-metadata title=x" {
		t.Errorf("ExtraOutputArgs = %q: the expert save made while Facebook answered was reverted",
			got.ExtraOutputArgs)
	}
	// The control: the backup was really added and stored.
	if !got.BackupIngestWanted || got.BackupURL == "" || got.BackupStreamKey == "" {
		t.Errorf("backup = %v / %q / %q, want the toggle on and the endpoint Facebook added stored",
			got.BackupIngestWanted, got.BackupURL, got.BackupStreamKey)
	}
	// And the answer describes the row as stored, backup included.
	var resp struct {
		Destination db.Destination `json:"destination"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Destination.BackupURL != got.BackupURL {
		t.Errorf("the response's backupUrl = %q, stored %q", resp.Destination.BackupURL, got.BackupURL)
	}
}

// THE WHOLE ROW IS WRITTEN BACK ONLY BY THE ROUTES WHOSE CHANGE IS THE ROW.
//
// store.UpdateDestination writes every column from the struct it is given,
// with no version behind it. That is right for the PUT, which decodes the
// operator's body over the row and stores what they saw, and for the level
// route, which edits the row in process under trackGainMu with nothing slow in
// between. It was wrong for Refresh stream key and the expert save, which went
// on writing the whole row after a platform call or a re-read, and reverted
// whatever landed in between. Those use the narrow writers now, and this keeps
// the next handler from taking the whole-row path without saying why here.
//
// It is the structural half of the expert save's fix: that window holds no
// platform call to land an edit inside, so there is no seam for a behavioural
// test at the HTTP level (internal/db pins the narrow write itself).
//
// Mutation: put saveExpertArgs back on `s.store.UpdateDestination(row)`.
// Observed red, naming saveExpertArgs.
func TestOnlyTheRoutesWhoseChangeIsTheRowWriteItAllBack(t *testing.T) {
	allowed := map[string]string{
		"handleUpdateDestination": "the PUT: the operator's body, decoded over the row, is the change",
		"persistTrackLevels":      "the level route and OBS scene rules: in process, under trackGainMu",
	}
	root := packageRoot(t)
	fset := token.NewFileSet()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	found := map[string]bool{}
	var unexpected []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(root, name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				return true
			}
			ast.Inspect(fn.Body, func(m ast.Node) bool {
				sel, ok := m.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "UpdateDestination" {
					return true
				}
				if inner, ok := sel.X.(*ast.SelectorExpr); ok && inner.Sel.Name == "store" {
					if _, ok := allowed[fn.Name.Name]; ok {
						found[fn.Name.Name] = true
					} else {
						unexpected = append(unexpected,
							fmt.Sprintf("%s: %s", fset.Position(sel.Pos()), fn.Name.Name))
					}
				}
				return true
			})
			return true
		})
	}
	// The positive control: the walk must find the two writers it allows, or a
	// pass here means the walk is broken.
	for fn := range allowed {
		if !found[fn] {
			t.Fatalf("the walk did not find %s calling store.UpdateDestination; fix the walk "+
				"(or, if that writer moved, this list) -- a pass would assert nothing", fn)
		}
	}
	if len(unexpected) > 0 {
		sort.Strings(unexpected)
		t.Fatalf("whole-row destination writes outside the routes whose change is the row:\n  %s\n\n"+
			"store.UpdateDestination writes back every column from the caller's copy, so anything "+
			"stored between that copy's read and this write is undone. Write only the columns this "+
			"handler decides (db.UpdateDestinationIngest, db.UpdateDestinationExpertArgs, or a new "+
			"narrow writer), or add the function here with the reason its window cannot hold one.",
			strings.Join(unexpected, "\n  "))
	}
}
