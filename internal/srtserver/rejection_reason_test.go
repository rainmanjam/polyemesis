package srtserver

import (
	"strings"
	"testing"

	srt "github.com/datarhei/gosrt"
	"github.com/datarhei/gosrt/packet"
)

// Every refusal must reach the publisher with the reason handleConnect chose.
//
// handleConnect sets a specific SRT rejection code on every refusal -- that is
// the whole difference between an encoder showing "wrong password" and one
// showing a generic failure. But the listener was run by gosrt's srt.Server,
// whose Serve loop answers a REJECT with req.Reject(REJ_PEER) unconditionally,
// so every one of those codes was overwritten and every publisher saw
// ERROR:PEER. Reproduced live with a garbage token: the server logged "token
// not recognised" while the client logged "rejected by peer".
//
// Real connections over a real socket, because the bug lived in the loop
// between the decision and the wire, which no unit test of handleConnect can see.
//
// Mutation: answer every REJECT with srt.REJ_PEER in the accept loop. Observed
// to fail on every row with "REJ_PEER".
func TestARefusedPublisherIsToldWhy(t *testing.T) {
	live := Target{SourceID: 1, Name: "live", Enabled: true, Sink: &recorder{}}
	disabled := Target{SourceID: 2, Name: "disabled", Enabled: false, Sink: &recorder{}}
	orphan := Target{SourceID: 3, Name: "orphan", Enabled: true}
	locked := Target{SourceID: 4, Name: "locked", Enabled: true, Sink: &recorder{}, Passphrase: "correct horse battery"}
	_, addr := serve(t, live, disabled, orphan, locked)

	// Occupy live's slot so a second publisher is refused as "already publishing".
	first, err := dial(t, addr, tokenFor(live))
	if err != nil {
		t.Fatalf("the first publisher was refused: %v", err)
	}
	t.Cleanup(func() { first.Close() })

	for _, tc := range []struct {
		name     string
		streamID string
		want     srt.RejectionReason
	}{
		{"an unrecognised token", "not-a-real-token", srt.REJ_BADSECRET},
		{"a disabled source", tokenFor(disabled), srt.REJ_CLOSE},
		{"a source with no pipeline", tokenFor(orphan), srt.REJ_RESOURCE},
		{"an unencrypted publish to a source that requires encryption", tokenFor(locked), srt.REJ_UNSECURE},
		{"a second publisher into a live slot", tokenFor(live), srt.REJ_RESOURCE},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn, err := dial(t, addr, tc.streamID)
			if err == nil {
				conn.Close()
				t.Fatalf("%s was accepted", tc.name)
			}
			want := packet.HandshakeType(tc.want).String()
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("%s was refused with %q; the publisher must see code %d (%s), "+
					"not REJ_PEER %d (%s), the generic code the reason was overwritten with",
					tc.name, err, tc.want, want, srt.REJ_PEER, packet.HandshakeType(srt.REJ_PEER).String())
			}
		})
	}
}
