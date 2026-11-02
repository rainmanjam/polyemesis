package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/secrets"
)

func doJSON(t *testing.T, h http.Handler, sign func(*http.Request), method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	r := jsonRequest(t, method, path, body)
	sign(r)
	return do(t, h, r)
}

func storageBody(t *testing.T, h http.Handler, sign func(*http.Request), path string) string {
	t.Helper()
	_, b := rawBodyOf(t, h, sign, path)
	return b
}

// storageFixtureBox is the secret box renditionServer gives the Server: same
// key, so a target planted straight into the store opens in the handlers.
func storageFixtureBox(t *testing.T) *secrets.Box {
	t.Helper()
	box, err := secrets.New([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	return box
}

// storageSecretPlant is the storage target's secret access key in the leak
// fixture. A sentence, not a random string, so gitleaks has nothing to flag.
const storageSecretPlant = "this is the planted storage secret and no response may carry it"

// TestTheStorageSecretAppearsInNoResponse drives every storage route as the
// admin session -- the principal who sees the most -- and requires the secret
// in none of them, while the access key id IS in the admin listing (so the
// body read was real) and is NOT in a read token's.
func TestTheStorageSecretAppearsInNoResponse(t *testing.T) {
	h, store, sign := plantedServer(t)
	_ = store
	read := createScopedToken(t, h, sign, "storage-read", db.ScopeRead)

	adminList := storageBody(t, h, sign, "/api/v1/storage/targets")
	if !strings.Contains(adminList, sentinelStorageKeyID) {
		t.Fatalf("admin listing lacks the access key id, so this read proved nothing: %s", adminList)
	}
	for _, path := range []string{"/api/v1/storage/targets", "/api/v1/storage/targets/1"} {
		for who, body := range map[string]string{"admin": storageBody(t, h, sign, path), "read": storageBody(t, h, bearer(read), path)} {
			if strings.Contains(body, storageSecretPlant) {
				t.Errorf("%s %s carries the secret access key", who, path)
			}
			if who == "read" && strings.Contains(body, sentinelStorageKeyID) {
				t.Errorf("read token sees the access key id on %s", path)
			}
			if !strings.Contains(body, `"hasSecret":true`) {
				t.Errorf("%s %s does not say a secret is set: %s", who, path, body)
			}
		}
	}

	// Saving without a secret keeps it; the update response carries none.
	rec := doJSON(t, h, sign, http.MethodPut, "/api/v1/storage/targets/1", map[string]any{"name": "archive-two"})
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), storageSecretPlant) {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	tg, err := store.GetStorageTarget(storageFixtureBox(t), 1)
	if err != nil || tg.SecretAccessKey != storageSecretPlant || tg.Name != "archive-two" {
		t.Fatalf("stored %+v %v", tg.Name, err)
	}
}

func TestStorageTargetCRUDValidatesAndRefusesPrivateEndpoints(t *testing.T) {
	h, _, sign := sourceServer(t)
	body := map[string]any{"name": "r2", "provider": "r2", "endpoint": "https://acct.r2.example",
		"bucket": "media-archive", "region": "auto", "accessKeyId": "fake-access-key",
		"secretAccessKey": "fake-secret-value-not-real", "mode": "move", "keepLocalHours": 12}
	rec := doJSON(t, h, sign, http.MethodPost, "/api/v1/storage/targets", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "fake-secret-value-not-real") {
		t.Fatal("create response echoes the secret")
	}
	var created struct {
		ID        int64  `json:"id"`
		Mode      string `json:"mode"`
		HasSecret bool   `json:"hasSecret"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if created.ID == 0 || created.Mode != "move" || !created.HasSecret {
		t.Fatalf("%+v", created)
	}
	for name, mut := range map[string]func(map[string]any){
		"private literal ip": func(b map[string]any) { b["endpoint"] = "http://10.0.0.5:9000" },
		"userinfo":           func(b map[string]any) { b["endpoint"] = "https://user:pw@s3.example" },
		"bad bucket":         func(b map[string]any) { b["bucket"] = "UPPER" },
		"bad mode":           func(b map[string]any) { b["mode"] = "sync" },
		"bad template":       func(b map[string]any) { b["prefixTemplate"] = "../escape" },
		"no secret":          func(b map[string]any) { delete(b, "secretAccessKey") },
	} {
		b := map[string]any{}
		for k, v := range body {
			b[k] = v
		}
		mut(b)
		if rec := doJSON(t, h, sign, http.MethodPost, "/api/v1/storage/targets", b); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	// The private address is allowed on purpose with the opt-in.
	b := map[string]any{}
	for k, v := range body {
		b[k] = v
	}
	b["endpoint"], b["allowPrivateTarget"], b["provider"] = "http://10.0.0.5:9000", true, "minio"
	if rec := doJSON(t, h, sign, http.MethodPost, "/api/v1/storage/targets", b); rec.Code != http.StatusCreated {
		t.Fatalf("opted-in private: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, h, sign, http.MethodDelete, "/api/v1/storage/targets/1", nil); rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
}

func TestTheStorageTestRouteIsSessionOnly(t *testing.T) {
	h, _, sign := sourceServer(t)
	admin := createScopedToken(t, h, sign, "storage-admin", db.ScopeAdmin)
	rec := doJSON(t, h, bearer(admin), http.MethodPost, "/api/v1/storage/targets/test", map[string]any{})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("admin bearer reached the Test route: %d", rec.Code)
	}
	// A session reaches it; an unroutable private endpoint without the opt-in
	// is refused before any request leaves.
	rec = doJSON(t, h, sign, http.MethodPost, "/api/v1/storage/targets/test", map[string]any{
		"name": "t", "provider": "minio", "endpoint": "http://127.0.0.1:1", "bucket": "bkt",
		"accessKeyId": "fake-access-key", "secretAccessKey": "fake-secret-value-not-real"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("session: %d %s", rec.Code, rec.Body.String())
	}
}

// The saved secret is bound to the endpoint, bucket and private-address
// opt-in it was saved with. Pointing a Test or a save anywhere else needs the
// secret typed again.
func TestTheSavedSecretOnlyGoesWhereItWasSavedFor(t *testing.T) {
	h, _, sign := sourceServer(t)
	rec := doJSON(t, h, sign, http.MethodPost, "/api/v1/storage/targets", map[string]any{
		"name": "b2", "provider": "b2", "endpoint": "https://s3.b2.example", "bucket": "media-archive",
		"accessKeyId": "fake-access-key", "secretAccessKey": "fake-secret-value-not-real"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)

	rebinds := map[string]map[string]any{
		"endpoint":           {"endpoint": "https://elsewhere.example"},
		"bucket":             {"bucket": "someone-elses-bucket"},
		"allowPrivateTarget": {"allowPrivateTarget": true},
	}
	for name, change := range rebinds {
		t.Run("test/"+name, func(t *testing.T) {
			body := map[string]any{"id": created.ID}
			for k, v := range change {
				body[k] = v
			}
			rec := doJSON(t, h, sign, http.MethodPost, "/api/v1/storage/targets/test", body)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "secret_required") {
				t.Fatalf("saved secret lent to a changed %s: %d %s", name, rec.Code, rec.Body.String())
			}
			body["secretAccessKey"] = "another-fake-secret"
			if rec := doJSON(t, h, sign, http.MethodPost, "/api/v1/storage/targets/test", body); rec.Code != http.StatusOK {
				t.Fatalf("with a typed secret: %d %s", rec.Code, rec.Body.String())
			}
		})
		t.Run("update/"+name, func(t *testing.T) {
			rec := doJSON(t, h, sign, http.MethodPut, "/api/v1/storage/targets/"+strconv.FormatInt(created.ID, 10), change)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "secret_required") {
				t.Fatalf("saved secret kept across a %s change: %d %s", name, rec.Code, rec.Body.String())
			}
		})
	}
	// Unchanged address: the saved secret is used (the probe runs and reports
	// a step, rather than the handler asking for the secret).
	if rec := doJSON(t, h, sign, http.MethodPost, "/api/v1/storage/targets/test", map[string]any{"id": created.ID}); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), `"steps"`) {
		t.Fatalf("same address: %d %s", rec.Code, rec.Body.String())
	}
	// A save that changes nothing about the address keeps the secret.
	if rec := doJSON(t, h, sign, http.MethodPut, "/api/v1/storage/targets/"+strconv.FormatInt(created.ID, 10),
		map[string]any{"name": "renamed", "endpoint": "https://S3.B2.example/"}); rec.Code != http.StatusOK {
		t.Fatalf("rename: %d %s", rec.Code, rec.Body.String())
	}
	// And a save that moves it, with the secret typed, is accepted.
	if rec := doJSON(t, h, sign, http.MethodPut, "/api/v1/storage/targets/"+strconv.FormatInt(created.ID, 10),
		map[string]any{"bucket": "new-bucket", "secretAccessKey": "another-fake-secret"}); rec.Code != http.StatusOK {
		t.Fatalf("move with secret: %d %s", rec.Code, rec.Body.String())
	}
}
