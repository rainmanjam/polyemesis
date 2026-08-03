package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// A settings save must not hold the STORE's settings lock while it waits for
// the network.
//
// db.UpdateSettings holds that lock across read-mutate-validate-write, and
// handlePutSettings decodes inside it -- it has to, because decoding over the
// stored document is what makes a partial payload safe. Reading the body in
// there would hold the lock for as long as the body took to arrive, and the
// only timeout the server sets is ReadHeaderTimeout, which bounds the headers
// and not the body (cmd/polyemesis/main.go).
//
// What that costs is not one slow request: it is every other settings writer in
// the install, and the one that matters is the scheduler's sweep. A schedule
// firing while an operator's phone drops mid-save would block the engine's
// goroutine on a lock held by a connection nobody is watching.
//
// So the body is buffered before any lock is taken. This test asserts that by
// proving the store is still usable while a save sits mid-body.
//
// Mutation that must make it fail: move the readJSONBody call in
// handlePutSettings back inside the db.UpdateSettings closure (decodeJSON
// rather than decodeJSONFrom). Measured: the UpdateSettings below then never
// returns and the test fails on its deadline.
func TestASlowSettingsBodyDoesNotHoldTheStoreSettingsLock(t *testing.T) {
	s, _, store := testServer(t, config.Config{})

	// A body that has started but not finished. The pipe is the synchronisation
	// point: the first Write does not return until the handler reads, so by the
	// time it does we know the handler is inside the body read rather than
	// still on its way there.
	pr, pw := io.Pipe()
	r := httptest.NewRequest(http.MethodPut, "/api/v1/settings", pr)
	r.Header.Set("Content-Type", "application/json")

	handlerDone := make(chan struct{})
	go func() {
		defer close(handlerDone)
		s.handlePutSettings(httptest.NewRecorder(), r)
	}()

	if _, err := pw.Write([]byte(`{"logging":`)); err != nil {
		t.Fatalf("write partial body: %v", err)
	}

	// The handler is now mid-body. The store must still be reachable.
	unblocked := make(chan error, 1)
	go func() {
		_, err := store.UpdateSettings(func(cur *db.Settings) error {
			cur.Failover.Playlist.Enabled = false
			return db.ErrSettingsUnchanged
		})
		unblocked <- err
	}()

	select {
	case err := <-unblocked:
		if err != nil {
			t.Fatalf("UpdateSettings during a partial save: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the store's settings lock was held by a request still waiting for " +
			"its body. Every other settings writer -- the scheduler's sweep above " +
			"all -- now waits on the client's connection.")
	}

	// Finish the request with something the decoder refuses, so the handler
	// answers 400 and returns before it reaches the manager reconcile that this
	// harness has no manager for.
	if _, err := pw.Write([]byte(`}`)); err != nil {
		t.Fatalf("write body remainder: %v", err)
	}
	if err := pw.Close(); err != nil {
		t.Fatalf("close body: %v", err)
	}
	select {
	case <-handlerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler did not return after its body finished")
	}
}
