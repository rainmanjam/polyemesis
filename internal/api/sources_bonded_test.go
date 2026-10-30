package api

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/srtla"
	"github.com/rainmanjam/polyemesis/internal/srtserver"
)

// A bonded encoder (#6) reaches the SRT listener from the SRTLA receiver's own
// socket, so the card's link is joined to its group by that socket's address:
// the listener's Peer is the group's UpstreamLocal. A direct SRT encoder has no
// group, and must not be shown one that merely shares a programme.
func TestTheSourceCardFindsTheBondedGroupBehindItsLink(t *testing.T) {
	groups := []srtla.GroupStats{
		{UpstreamLocal: "127.0.0.1:50001", Links: []srtla.LinkStats{{Peer: "198.51.100.7:4001"}}},
		{UpstreamLocal: "127.0.0.1:50002", Links: []srtla.LinkStats{{Peer: "198.51.100.8:4001"}, {Peer: "203.0.113.4:4002"}}},
	}
	got := bondedFor(&srtserver.LinkStats{Peer: "127.0.0.1:50002"}, groups)
	if got == nil || len(got.Links) != 2 {
		t.Fatalf("bondedFor = %+v, want the two-link group", got)
	}
	if bondedFor(&srtserver.LinkStats{Peer: "192.0.2.10:6000"}, groups) != nil {
		t.Error("a direct SRT encoder was given a bonded group")
	}
	if bondedFor(nil, groups) != nil {
		t.Error("no link, but a bonded group")
	}
}
