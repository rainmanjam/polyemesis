package api

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/srtserver"
)

// The primary's numbers are the ones the card means.
//
// Keying srtserver's live map by (SourceID, Backup) made two links for one
// source reachable, and the caller here still took the FIRST match. That is
// not a wrong answer so much as an unstable one: SRTLinks is built by ranging
// a map, so the same source could report the primary's bitrate on one refresh
// and the standby's on the next with nothing having changed.
//
// The backup is listed FIRST in this fixture on purpose. A version of this
// test with the primary first passes against the old first-match code, which
// makes it the guard that cannot fail -- it would agree with the bug.
//
// Mutation proving it can fail: in linkForCard, change `if !stat.Backup {`
// to `if true {`. Measured: FAIL, "picked the standby".
func TestTheSourceCardPrefersThePrimaryUplink(t *testing.T) {
	links := []srtserver.LinkStats{
		{SourceID: 7, Backup: true, Peer: "standby"},
		{SourceID: 7, Backup: false, Peer: "primary"},
	}
	got := linkForCard(links, 7)
	if got == nil {
		t.Fatal("no link for a source with two live publishers")
	}
	if got.Backup {
		t.Errorf("picked the standby (peer %q); the card shows one set of "+
			"numbers and the primary is the feed that is on air", got.Peer)
	}
}

// Falling back matters more than it looks. An operator whose primary has just
// dropped is looking at this card BECAUSE the standby is carrying the show; a
// blank uplink panel at that moment is the least useful thing it could do.
//
// Mutation proving it can fail: in linkForCard, change the final `return
// backup` to `return nil`. Measured: FAIL, "no link while the standby is live".
func TestAStandbyAloneStillReportsItsUplink(t *testing.T) {
	links := []srtserver.LinkStats{{SourceID: 7, Backup: true, Peer: "standby"}}
	got := linkForCard(links, 7)
	if got == nil {
		t.Fatal("no link while the standby is live and publishing; the uplink " +
			"panel goes blank at the exact moment an operator needs it")
	}
	if !got.Backup {
		t.Errorf("reported peer %q as the primary; it is the standby", got.Peer)
	}
}

// Another source's publisher is not this source's link. Cheap, but the loop
// carries a `continue` that a refactor can drop without any other test
// noticing.
//
// Mutation proving it can fail: in linkForCard, change
// `if l.SourceID != sourceID {` to `if false {`. Measured: FAIL.
func TestAnotherSourcesUplinkIsNotBorrowed(t *testing.T) {
	links := []srtserver.LinkStats{{SourceID: 9, Backup: false, Peer: "other"}}
	if got := linkForCard(links, 7); got != nil {
		t.Errorf("source 7 reported source 9's link (peer %q)", got.Peer)
	}
}
