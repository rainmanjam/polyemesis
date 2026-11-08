package oauth

import (
	"context"
	"strings"
	"testing"
)

// The count is X's string total_watching on the RUNNING broadcast, and it is asked for by name.
func TestXLiveViewersReadsTotalWatchingOfTheRunningBroadcast(t *testing.T) {
	f := newXFake(t)
	f.list = `{"data":[{"id":"old","state":"ENDED","total_watching":"99"},` +
		`{"id":"b1","state":"RUNNING","title":"Show","start_ms":"1755302400000","total_watching":"42"}]}`
	lv, err := f.x.LiveViewers(context.Background(), "tok")
	if err != nil {
		t.Fatalf("LiveViewers: %v", err)
	}
	if !lv.Live || lv.Viewers == nil || *lv.Viewers != 42 || lv.Title != "Show" || lv.StartedAt == nil {
		t.Errorf("LiveViewers = %+v, want live, 42 viewers, titled, started", lv)
	}
	if got := f.requests(); len(got) != 1 {
		t.Errorf("requests = %v, want one list read", got)
	}
	if !strings.Contains(xBroadcastFields, "total_watching") {
		t.Error("broadcast.fields does not ask for total_watching")
	}
}

// A count X did not send is nil, never zero, and a count that is not a number is treated the same.
func TestXLiveViewersKeepsAbsentCountsNil(t *testing.T) {
	for name, body := range map[string]string{
		"absent":   `{"data":[{"id":"b1","state":"RUNNING"}]}`,
		"garbage":  `{"data":[{"id":"b1","state":"RUNNING","total_watching":"many"}]}`,
		"negative": `{"data":[{"id":"b1","state":"RUNNING","total_watching":"-3"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newXFake(t)
			f.list = body
			lv, err := f.x.LiveViewers(context.Background(), "tok")
			if err != nil || !lv.Live || lv.Viewers != nil {
				t.Errorf("LiveViewers = %+v, %v; want live with no count", lv, err)
			}
		})
	}
}

// No RUNNING broadcast is not live, and the gate and a failed list are errors, not silence.
func TestXLiveViewersOfflineGatedAndRefused(t *testing.T) {
	f := newXFake(t)
	f.list = xEmptyBroadcastList
	lv, err := f.x.LiveViewers(context.Background(), "tok")
	if err != nil || lv.Live || lv.Viewers != nil {
		t.Errorf("empty list = %+v, %v; want not live", lv, err)
	}
	f.x = NewX(WithBaseURL(f.srv.URL))
	if _, err := f.x.LiveViewers(context.Background(), "tok"); err == nil {
		t.Error("a closed gate answered; want ErrXLivestreamOff")
	}
}

// The locale travels as a string in the go-live body and is left out when empty.
func TestXPublishSendsLocale(t *testing.T) {
	f := newXFake(t)
	if _, err := f.x.PublishBroadcast(context.Background(), "tok", xPendingHandle(xSourceID),
		XPublish{Locale: " fr "}); err != nil {
		t.Fatalf("PublishBroadcast: %v", err)
	}
	if got := f.requests(); len(got) != 1 || !strings.Contains(got[0], `"locale":"fr"`) {
		t.Errorf("go-live = %v, want locale fr", got)
	}
}
