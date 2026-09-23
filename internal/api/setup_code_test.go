package api

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/auth"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/db/dbtest"
	"github.com/rainmanjam/polyemesis/internal/secrets"
)

// freshInstall is a server over a store with NO user -- the state install.sh
// leaves a box in, with the port already open -- holding the code a real boot
// would have prepared.
func freshInstall(t *testing.T) (http.Handler, *db.DB, *auth.SetupCode) {
	t.Helper()
	store := dbtest.OpenCheap(t)
	code, err := auth.PrepareSetupCode(t.TempDir(), false, "")
	if err != nil {
		t.Fatal(err)
	}
	return freshInstallWith(t, store, code), store, code
}

func freshInstallWith(t *testing.T, store *db.DB, code *auth.SetupCode) http.Handler {
	t.Helper()
	box, err := secrets.New(bytes.Repeat([]byte{0x2a}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return New(Options{
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:        store,
		Secrets:   box,
		Version:   "test",
		SetupCode: code,
	}).Handler()
}

func postSetup(t *testing.T, h http.Handler, addr string, body map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := jsonRequest(t, http.MethodPost, "/api/v1/setup", body)
	r.RemoteAddr = addr
	return do(t, h, r)
}

// THE HOLE THIS CLOSES: on a fresh install with its port open, anyone who
// reached POST /setup first became the admin. Without the code a stranger now
// gets 403 and the install stays unclaimed for its operator.
func TestSetupWithoutTheCodeIsRefusedOnAFreshInstall(t *testing.T) {
	h, store, _ := freshInstall(t)

	for _, code := range []string{"", "WRONG-CODE-GUESS-0000"} {
		w := postSetup(t, h, "198.51.100.7:5000",
			map[string]string{"username": "intruder", "password": "Intruder!9xzq", "setupCode": code})
		if w.Code != http.StatusForbidden {
			t.Fatalf("setup with code %q: status %d, want 403 -- a stranger claimed the install: %s",
				code, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), auth.SetupCodeFile) {
			t.Errorf("refusal does not say where the code is: %s", w.Body.String())
		}
	}
	if has, _ := store.HasUser(); has {
		t.Fatal("an admin exists after only refused setup attempts")
	}
}

func TestSetupWithTheCodeCreatesTheAdminAndUsesTheCodeUp(t *testing.T) {
	h, store, code := freshInstall(t)
	value := code.Code()

	w := postSetup(t, h, "203.0.113.5:44444",
		map[string]string{"username": "admin", "password": testPassword, "setupCode": strings.ToLower(value)})
	if w.Code != http.StatusCreated {
		t.Fatalf("setup with the right code: status %d: %s", w.Code, w.Body.String())
	}
	if has, _ := store.HasUser(); !has {
		t.Fatal("no admin after a successful setup")
	}
	if code.Pending() {
		t.Error("the code is still pending after it created the admin")
	}
	if _, err := os.Stat(code.Path()); !os.IsNotExist(err) {
		t.Errorf("setup-code file survived setup (stat err %v)", err)
	}

	// The same code, again: the install is claimed, and says so.
	w = postSetup(t, h, "203.0.113.5:44444",
		map[string]string{"username": "second", "password": testPassword, "setupCode": value})
	if w.Code != http.StatusConflict {
		t.Fatalf("replayed setup: status %d, want 409: %s", w.Code, w.Body.String())
	}
}

// A server built with no code and no admin cannot check the code, so it must
// refuse -- not skip the check.
func TestSetupWithNoCodePreparedIsRefusedNotOpened(t *testing.T) {
	store := dbtest.OpenCheap(t)
	h := freshInstallWith(t, store, nil)

	w := postSetup(t, h, "203.0.113.5:44444",
		map[string]string{"username": "admin", "password": testPassword})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("setup with no code prepared: status %d, want 503: %s", w.Code, w.Body.String())
	}
	if has, _ := store.HasUser(); has {
		t.Fatal("an admin was created by a server that had no setup code")
	}
}

func TestSetupReportsAStoreThatCannotBeRead(t *testing.T) {
	h, store, code := freshInstall(t)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	w := postSetup(t, h, "203.0.113.5:44444",
		map[string]string{"username": "admin", "password": testPassword, "setupCode": code.Code()})
	if w.Code < 500 {
		t.Fatalf("setup against a closed store: status %d, want a 5xx", w.Code)
	}
}
