package oauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fbTargetStub serves the two reads AdoptLiveVideo makes: the target resolve and
// the live_videos edge.
func fbTargetStub(t *testing.T, videos []map[string]any) *Facebook {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/live_videos"):
			_ = json.NewEncoder(w).Encode(map[string]any{"data": videos})
		default: // /me and anything else the target resolve reads
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "9001", "name": "A Profile"})
		}
	}))
	t.Cleanup(srv.Close)
	return &Facebook{endpoints: newEndpoints([]ProviderOption{WithBaseURL(srv.URL)})}
}

// A HAND-PASTED KEY ADOPTS THE BROADCAST THE ACCOUNT IS RUNNING. #725.
//
// A key polyemesis fetched carries the live-video id inside it. A persistent key
// pasted from Live Producer does not, so chat, metadata and End broadcast had
// nothing to attach to. Going live with that key creates a live video on the
// same target the connected account can see, and Facebook's own limit -- one
// live video at a time per persistent key -- is what makes "the broadcast on
// this target" a fact rather than a guess while it is publishing.
func TestAPastedKeyAdoptsTheOneBroadcastOnAir(t *testing.T) {
	f := fbTargetStub(t, []map[string]any{
		{"id": "555", "status": "LIVE", "title": "Tonight's show"},
	})

	got, err := f.AdoptLiveVideo(context.Background(), "tok", "")
	if err != nil {
		t.Fatalf("AdoptLiveVideo: %v", err)
	}
	if got != "555" {
		t.Errorf("adopted %q, want 555", got)
	}
}

// AND REFUSES WHEN IT CANNOT TELL. Nothing on a live video says polyemesis
// started it, and a target may perfectly well carry a broadcast another tool is
// running. Two at the same status is a question this process cannot answer.
func TestAdoptionRefusesTwoBroadcastsAtTheSameStatus(t *testing.T) {
	f := fbTargetStub(t, []map[string]any{
		{"id": "555", "status": "LIVE", "title": "Tonight's show"},
		{"id": "556", "status": "LIVE", "title": "Somebody else's stream"},
	})

	_, err := f.AdoptLiveVideo(context.Background(), "tok", "")
	if err == nil {
		t.Fatal("adopted one of two live broadcasts. A title written onto the wrong " +
			"one is published to somebody's viewers under a heading meant for another " +
			"stream, and nothing in the response would say so")
	}
	// THE OPERATOR HAS TO BE ABLE TO GO AND LOOK. "There are two" without
	// saying which two is a refusal they cannot act on.
	for _, want := range []string{"Tonight's show", "Somebody else's stream"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q, so the operator cannot find what it "+
				"is refusing to choose between:\n%v", want, err)
		}
	}
}

// A LIVE ONE OUTRANKS A STAGED ONE, so the ordinary case of a scheduled
// broadcast sitting beside the running one is not an ambiguity.
func TestAStagedBroadcastDoesNotMakeALiveOneAmbiguous(t *testing.T) {
	f := fbTargetStub(t, []map[string]any{
		{"id": "700", "status": "SCHEDULED_UNPUBLISHED", "title": "Next week"},
		{"id": "555", "status": "LIVE", "title": "On air now"},
	})

	got, err := f.AdoptLiveVideo(context.Background(), "tok", "")
	if err != nil {
		t.Fatalf("a staged broadcast beside a live one was treated as ambiguous: %v", err)
	}
	if got != "555" {
		t.Errorf("adopted %q, want the live one (555)", got)
	}
}

// A FINISHED BROADCAST IS NOT A CANDIDATE AT ALL. fbLiveRank's own comment:
// "editing last week's broadcast because it sorted first is the most
// embarrassing outcome available here."
func TestAFinishedBroadcastIsNeverAdopted(t *testing.T) {
	// EXACTLY ONE, and that is the point of the fixture. With two finished
	// broadcasts the ambiguity check refuses them anyway, so the test would pass
	// against a build that had stopped excluding VODs altogether -- which is
	// what a mutation proved before this was written this way. One VOD and
	// nothing else is the only shape that isolates the exclusion.
	f := fbTargetStub(t, []map[string]any{
		{"id": "111", "status": "VOD", "title": "Last week"},
	})

	_, err := f.AdoptLiveVideo(context.Background(), "tok", "")
	if err == nil {
		t.Fatal("adopted a finished VOD. Chat would attach to last week's comment " +
			"thread and a metadata push would retitle a broadcast that has already " +
			"aired -- to everyone who has it saved")
	}
	if !strings.Contains(err.Error(), "no live or staged broadcast") {
		t.Errorf("a target holding only finished broadcasts should read as idle: %v", err)
	}
}

// The same shape for a single unrecognised status, since fbLiveRank sends
// everything it does not know to the same place as a VOD.
func TestASingleUnrecognisedStatusIsNotAdopted(t *testing.T) {
	f := fbTargetStub(t, []map[string]any{
		{"id": "222", "status": "SCHEDULED_CANCELED", "title": "Called off"},
	})

	if _, err := f.AdoptLiveVideo(context.Background(), "tok", ""); err == nil {
		t.Fatal("adopted a cancelled broadcast")
	}
}

func TestAdoptionSaysSoWhenNothingIsLive(t *testing.T) {
	f := fbTargetStub(t, nil)

	_, err := f.AdoptLiveVideo(context.Background(), "tok", "")
	if err == nil {
		t.Fatal("adopted something from an empty target")
	}
	if !strings.Contains(err.Error(), "no live or staged broadcast") {
		t.Errorf("the message does not say the target is idle: %v", err)
	}
}

// A TARGET THAT WILL NOT RESOLVE IS REPORTED AS SUCH, not as an empty result.
// Adoption asks two questions of the platform and the first one can fail on its
// own -- a revoked token, a Page the account no longer manages -- and "there is
// no broadcast" would be the wrong answer to give for it.
func TestAdoptionReportsATargetItCannotResolve(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid OAuth access token","code":190}}`))
	}))
	t.Cleanup(srv.Close)
	f := &Facebook{endpoints: newEndpoints([]ProviderOption{WithBaseURL(srv.URL)})}

	id, err := f.AdoptLiveVideo(context.Background(), "stale", "1234")
	if err == nil {
		t.Fatal("a target that could not be resolved reported success")
	}
	if id != "" {
		t.Errorf("an id came back from a failed resolve: %q", id)
	}
}
