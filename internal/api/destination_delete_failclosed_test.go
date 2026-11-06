package api

import (
	"net/http"
	"testing"
)

// The "this delete ends a live broadcast" confirmation is a guard, so a failed
// read of the row must not turn it into an unconfirmed delete. A row whose
// lifecycle state cannot be read could be carrying a broadcast; the delete
// needs confirm:true, and without it the row stays.
func TestDestinationDeleteFailsClosedWhenTheRowCannotBeRead(t *testing.T) {
	_, h, store, sign := engineServer(t, defaultTools(), Options{})
	d := createDestination(t, h, sign, destinationBody("A", false, nil))

	if _, err := store.SQL().Exec(`UPDATE destinations SET lifecycle='{not json' WHERE id=?`, d.ID); err != nil {
		t.Fatal(err)
	}

	send(t, h, sign, http.MethodDelete, "/api/v1/destinations/"+itoa(d.ID), nil, http.StatusBadRequest)

	var n int
	if err := store.SQL().QueryRow(`SELECT COUNT(*) FROM destinations WHERE id=?`, d.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Error("the destination was deleted although its broadcast state could not be read")
	}

	// The row is corrupt, not live: confirming deletes it, so it is not stuck.
	send(t, h, sign, http.MethodDelete, "/api/v1/destinations/"+itoa(d.ID),
		map[string]any{"confirm": true}, http.StatusOK)
	if err := store.SQL().QueryRow(`SELECT COUNT(*) FROM destinations WHERE id=?`, d.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("a confirmed delete left the unreadable row in place")
	}
}
