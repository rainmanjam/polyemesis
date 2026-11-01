package api

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// The optional chat notice before a planned rollover (#19). It is off by default, because a viewer should
// not see an operator's machinery unless the operator asked for it. When on, one line goes to the chat of
// the platform being rolled over, just before the platform call. A failed notice never blocks or fails the
// rollover: the stream is the thing that matters, and a chat line is best effort.

// chatRecorder captures every chat notice a rollover sends, in order, together with the restarts, so a test
// can see that the notice came first.
type chatRecorder struct {
	calls []string
	order []string
	err   error
}

// withChatNotice wires a recording notice into the rig's coordinator. It is attached to the host after the
// rig is built, because the host is a value on the coordinator.
func withChatNotice(r *rolloverRig, rec *chatRecorder) {
	r.c.host.chatNotice = func(_ context.Context, p db.Platform, _ string, text string) error {
		rec.calls = append(rec.calls, string(p)+": "+text)
		rec.order = append(rec.order, "chat")
		return rec.err
	}
	baseRestart := r.c.host.restart
	r.c.host.restart = func(id int64) error {
		rec.order = append(rec.order, "restart")
		return baseRestart(id)
	}
}

// aDueTwitchRig is a Twitch destination on air far past its cap, so one sweep reconnects it.
func aDueTwitchRig(t *testing.T, notice bool) *rolloverRig {
	t.Helper()
	r := newRolloverRig(t)
	r.addDest(1, db.PlatformTwitch, "")
	r.enable(1, "")
	r.store.settings[1] = db.RolloverSettings{DestinationID: 1, Enabled: true, MarginMinutes: 30,
		SessionStartedAt: r.now.Add(-400 * time.Hour), ChatNotice: notice}
	return r
}

// The default is off: a rollover with the setting left alone sends nothing to chat.
func TestTheRolloverChatNoticeIsOffByDefaultAndSendsNothing(t *testing.T) {
	r := aDueTwitchRig(t, false)
	rec := &chatRecorder{}
	withChatNotice(r, rec)

	r.c.sweepOnce(context.Background())

	if len(rec.calls) != 0 {
		t.Fatalf("a rollover with the notice off sent %d chat lines, want none", len(rec.calls))
	}
	if len(r.restarts) != 1 {
		t.Fatalf("restarts = %v, want the rollover to happen", r.restarts)
	}
}

// When the operator turns it on, exactly one notice goes out, and it goes out BEFORE the platform call.
func TestTheRolloverChatNoticeIsSentOnceAndBeforeTheRollover(t *testing.T) {
	r := aDueTwitchRig(t, true)
	rec := &chatRecorder{}
	withChatNotice(r, rec)

	r.c.sweepOnce(context.Background())

	if len(rec.calls) != 1 {
		t.Fatalf("chat notices = %d, want exactly one", len(rec.calls))
	}
	if !strings.HasPrefix(rec.calls[0], "twitch: ") {
		t.Fatalf("notice went to %q, want the rolled-over platform only", rec.calls[0])
	}
	if len(rec.order) != 2 || rec.order[0] != "chat" || rec.order[1] != "restart" {
		t.Fatalf("order = %v, want the notice before the restart", rec.order)
	}
}

// A notice that fails to send is logged and the rollover still happens.
func TestAFailedRolloverChatNoticeStillRollsOver(t *testing.T) {
	r := aDueTwitchRig(t, true)
	rec := &chatRecorder{err: errors.New("chat is not connected")}
	withChatNotice(r, rec)

	r.c.sweepOnce(context.Background())

	if len(rec.calls) != 1 {
		t.Fatalf("chat notices = %d, want the attempt made once", len(rec.calls))
	}
	if len(r.restarts) != 1 || r.restarts[0] != 1 {
		t.Fatalf("restarts = %v, want the rollover to proceed after a failed notice", r.restarts)
	}
}

// The notice names no credential. The token the rollover uses to reach the platform never appears in the
// text, even though it is in scope when the notice is built.
func TestTheRolloverChatNoticeCarriesNoCredential(t *testing.T) {
	r := aDueTwitchRig(t, true)
	secret := "cred" + "-" + "xyz123-do-not-echo"
	r.c.host.tokenFor = func(context.Context, int64) (*db.PlatformAccount, error) {
		return &db.PlatformAccount{AccessToken: secret}, nil
	}
	rec := &chatRecorder{}
	withChatNotice(r, rec)

	r.c.sweepOnce(context.Background())

	for _, c := range rec.calls {
		if strings.Contains(c, secret) {
			t.Fatalf("chat notice %q carries the credential", c)
		}
	}
}
