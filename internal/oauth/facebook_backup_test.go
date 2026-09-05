package oauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fbBackupStub serves the target resolve plus whatever the input_streams create
// and the follow-up video read should answer with.
type fbBackupStub struct {
	mu sync.Mutex
	// createBody is what POST /{id}/input_streams returns.
	createBody map[string]any
	// videoBody is what GET /{id} returns, for the fill-the-gap path.
	videoBody map[string]any
	// paths records every path called, so a test can assert which reads
	// happened rather than only what came back.
	paths []string
}

func (b *fbBackupStub) provider(t *testing.T) *Facebook {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		b.paths = append(b.paths, r.Method+" "+r.URL.Path)
		create, video := b.createBody, b.videoBody
		b.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/input_streams"):
			_ = json.NewEncoder(w).Encode(create)
		case strings.HasSuffix(r.URL.Path, "/accounts"):
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
		case strings.HasSuffix(r.URL.Path, "/me"):
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "9001", "name": "A Profile"})
		default:
			if video != nil {
				_ = json.NewEncoder(w).Encode(video)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "9001", "name": "A Profile"})
		}
	}))
	t.Cleanup(srv.Close)
	return &Facebook{endpoints: newEndpoints([]ProviderOption{WithBaseURL(srv.URL)})}
}

func (b *fbBackupStub) called(sub string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, p := range b.paths {
		if strings.Contains(p, sub) {
			return true
		}
	}
	return false
}

// A BACKUP CAN BE ADDED TO A BROADCAST THAT ALREADY EXISTS. #727.
//
// enable_backup_ingest is a CREATE parameter, so before this the only route to
// a backup endpoint was "Refresh key" -- which starts a new live video and
// discards the one the destination is configured against, along with its
// comment thread and its title. The remedy cost more than the problem.
func TestABackupIngestIsAddedToTheExistingBroadcast(t *testing.T) {
	stub := &fbBackupStub{createBody: map[string]any{
		"id":                "77-1",
		"stream_id":         "1",
		"secure_stream_url": "rtmps://live-api-s.facebook.com:443/rtmp/FB-77-1-secondkey",
	}}
	f := stub.provider(t)

	ing, err := f.AddBackupIngest(context.Background(), "tok", "", "77")
	if err != nil {
		t.Fatalf("AddBackupIngest: %v", err)
	}
	if !stub.called("/77/input_streams") {
		t.Errorf("the backup was not requested on the existing video: %v", stub.paths)
	}
	// NO NEW BROADCAST. That is the entire point: a create here would be the
	// same destructive remedy this replaces.
	if stub.called("/live_videos") {
		t.Errorf("a new live video was created; the existing broadcast should have been "+
			"modified in place: %v", stub.paths)
	}
	if ing.Key != "FB-77-1-secondkey" {
		t.Errorf("key = %q, want the backup's own key", ing.Key)
	}
	if !strings.HasPrefix(ing.URL, "rtmps://") {
		t.Errorf("url = %q, want the RTMPS server half", ing.URL)
	}
}

// WHEN THE CREATE COMES BACK THIN, one read of the video fills the gap -- the
// same shape IngestFor already uses, because Graph does not always return the
// ingest on the object it just made.
func TestAThinBackupCreateIsFilledByReadingTheVideo(t *testing.T) {
	stub := &fbBackupStub{
		createBody: map[string]any{"id": "77-1", "stream_id": "1"},
		videoBody: map[string]any{
			"id": "77",
			"secure_stream_secondary_urls": []string{
				"rtmps://live-api-s.facebook.com:443/rtmp/FB-77-1-fromtheread",
			},
		},
	}
	f := stub.provider(t)

	ing, err := f.AddBackupIngest(context.Background(), "tok", "", "77")
	if err != nil {
		t.Fatalf("AddBackupIngest: %v", err)
	}
	if ing.Key != "FB-77-1-fromtheread" {
		t.Errorf("key = %q, want the one read back from the video", ing.Key)
	}
}

// NO ID, NO CALL. A hand-pasted key carries no live-video id, and adding an
// ingest to a broadcast this process cannot name would be a write against
// something that may not be ours.
func TestABackupIsNotAddedWithoutALiveVideoID(t *testing.T) {
	stub := &fbBackupStub{}
	f := stub.provider(t)

	if _, err := f.AddBackupIngest(context.Background(), "tok", "", "  "); err == nil {
		t.Fatal("a backup was requested with no live video id")
	}
	if len(stub.paths) != 0 {
		t.Errorf("the platform was called anyway: %v", stub.paths)
	}
}

// AND A REFUSAL IS AN ERROR, not a silently missing feed: the caller turns it
// into a warning that says the destination is otherwise saved.
func TestABackupThatFacebookRefusesIsReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/input_streams") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Backup streams cannot be added once live","code":100}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"9001","name":"A Profile"}`))
	}))
	t.Cleanup(srv.Close)
	f := &Facebook{endpoints: newEndpoints([]ProviderOption{WithBaseURL(srv.URL)})}

	if _, err := f.AddBackupIngest(context.Background(), "tok", "", "77"); err == nil {
		t.Fatal("a refused backup reported success")
	}
}

// The refusal arms, each of which is a different thing going wrong and a
// different sentence the operator ends up reading. They are here because a
// backup that silently does not appear is the failure #727 exists to end, and
// every one of these is a route to exactly that.
func TestTheBackupIngestRefusalsAreDistinct(t *testing.T) {
	for _, tc := range []struct {
		name string
		// serve is the whole stub: each case breaks a different step.
		serve func(w http.ResponseWriter, r *http.Request)
		// target is the Page ref, empty for the profile.
		target string
		want   string
	}{
		{
			// THE TARGET ITSELF. A revoked token or a Page the account no
			// longer manages fails before the backup is ever requested.
			name:   "the target will not resolve",
			target: "1234",
			serve: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"message":"Invalid OAuth access token","code":190}}`))
			},
			want: "",
		},
		{
			// THE CREATE SUCCEEDS AND SAYS NOTHING, and the follow-up read of
			// the video fails too. Both halves have to fail for this arm.
			name: "the create is thin and the video cannot be read back",
			serve: func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/input_streams"):
					writeJSONFor(w, map[string]any{"id": "77-1"})
				case strings.HasSuffix(r.URL.Path, "/me"),
					strings.HasSuffix(r.URL.Path, "/accounts"):
					writeJSONFor(w, map[string]any{"id": "9001", "name": "A Profile"})
				default:
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = w.Write([]byte(`{"error":{"message":"temporarily unavailable","code":2}}`))
				}
			},
			// NO WORDING ASSERTED. fbAdvice passes a 5xx through unwrapped --
			// its added context is for the refusals it can recognise -- so what
			// reaches the operator here is the transport error naming the
			// endpoint and the status. That is informative and is not this
			// test's to change; what matters is that the step fails rather than
			// producing a silent half-configured backup.
			want: "",
		},
		{
			// BOTH READS SUCCEED AND NEITHER CARRIES AN INGEST. Facebook
			// accepted the request and produced nothing usable, which is not
			// an error anywhere in the transport and must not read as success.
			name: "nothing anywhere carries an ingest url",
			serve: func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/input_streams"):
					writeJSONFor(w, map[string]any{"id": "77-1"})
				case strings.HasSuffix(r.URL.Path, "/accounts"):
					writeJSONFor(w, map[string]any{"data": []any{}})
				case strings.HasSuffix(r.URL.Path, "/me"):
					writeJSONFor(w, map[string]any{"id": "9001", "name": "A Profile"})
				default:
					writeJSONFor(w, map[string]any{"id": "77"})
				}
			},
			want: "returned no URL",
		},
		{
			// AN INGEST THAT WILL NOT SPLIT. Destination.Target() joins the
			// server and key back with a single slash, so a URL this cannot
			// split reversibly must be refused rather than stored in halves
			// that do not reassemble.
			name: "the ingest url cannot be split into server and key",
			serve: func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/input_streams"):
					writeJSONFor(w, map[string]any{"id": "77-1", "secure_stream_url": "not-a-url"})
				default:
					writeJSONFor(w, map[string]any{"id": "9001", "name": "A Profile"})
				}
			},
			want: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(tc.serve))
			t.Cleanup(srv.Close)
			f := &Facebook{endpoints: newEndpoints([]ProviderOption{WithBaseURL(srv.URL)})}

			// A NAMED TARGET (a Page) rather than the profile, so the resolve
			// is a real read that can fail on its own. With an empty ref the
			// profile is assumed without asking anybody, and the first case
			// below would fail one step later than it means to.
			ing, err := f.AddBackupIngest(context.Background(), "tok", tc.target, "77")
			if err == nil {
				t.Fatalf("reported success; a backup that silently does not appear is the "+
					"failure this exists to end (got %+v)", ing)
			}
			if ing != nil {
				t.Errorf("an ingest came back alongside the error: %+v", ing)
			}
			if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the error does not say which step failed (want %q): %v", tc.want, err)
			}
		})
	}
}

func writeJSONFor(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
