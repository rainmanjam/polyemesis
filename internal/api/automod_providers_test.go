package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/automod"
	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/db/dbtest"
	"github.com/rainmanjam/polyemesis/internal/diag"
	"github.com/rainmanjam/polyemesis/internal/secrets"
)

// fakeKey is built at runtime, so the source holds no credential-shaped
// literal for a scanner to trip on -- and so a test can search every output
// for exactly this string.
func fakeKey(tag string) string { return "byok-" + tag + "-" + strings.Repeat("z", 12) }

// smartOpenAI is a fake OpenAI-compatible endpoint that behaves like a working
// moderation model: it flags the probe's abusive line and nothing else. It
// also records every Authorization header it was sent.
func smartOpenAI(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var auths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		var body struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		abusive := len(body.Messages) > 1 && strings.Contains(body.Messages[1].Content, "worthless")
		inner, _ := json.Marshal(map[string]any{"abusive": abusive, "confidence": 0.95,
			"category": "harassment", "reason": "fake"})
		json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{
			{"message": map[string]string{"content": string(inner)}}}})
	}))
	t.Cleanup(srv.Close)
	return srv, &auths
}

func testBody(endpoint string) map[string]any {
	return map[string]any{"checker": "secondary", "protocol": "openai", "preset": "custom",
		"endpoint": endpoint, "model": "fake-model", "timeoutSeconds": 4}
}

// putSecondary GETs the settings, applies change to the secondary block, and
// PUTs them back, expecting want.
func putSecondary(t *testing.T, h http.Handler, sign func(*http.Request), want int, change func(map[string]any)) []byte {
	t.Helper()
	var doc map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK), &doc)
	sec := doc["automod"].(map[string]any)["secondary"].(map[string]any)
	change(sec)
	return send(t, h, sign, http.MethodPut, "/api/v1/settings", doc, want)
}

func secondaryOf(t *testing.T, h http.Handler, sign func(*http.Request)) db.AutomodModel {
	t.Helper()
	var doc struct {
		Automod db.AutomodSettings `json:"automod"`
	}
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK), &doc)
	return doc.Automod.Secondary
}

// THE TEST-BEFORE-ENABLE RULE, enforced by the server. A checker that fails
// open on every message looks exactly like one that approves of everything,
// so it cannot be switched on until the Test button has seen it answer -- for
// the connection being enabled, with the key it will use.
func TestAModelCheckerCannotBeEnabledUntilItsTestPassesForThatConnection(t *testing.T) {
	h, _, sign := sourceServer(t)
	fake, _ := smartOpenAI(t)
	point := func(m map[string]any) {
		m["protocol"], m["preset"], m["endpoint"], m["model"] = "openai", "custom", fake.URL, "fake-model"
	}

	// Untested: refused, and saying why.
	body := putSecondary(t, h, sign, http.StatusBadRequest, func(m map[string]any) { point(m); m["enabled"] = true })
	if !strings.Contains(string(body), "run Test on the secondary") {
		t.Fatalf("refusal does not say what to do: %s", body)
	}
	// A client cannot forge the pass by sending tested:true.
	putSecondary(t, h, sign, http.StatusBadRequest, func(m map[string]any) {
		point(m)
		m["enabled"], m["tested"] = true, true
	})

	// The Test passes against a working endpoint.
	var res automodTestResponse
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/automod/test", testBody(fake.URL), http.StatusOK), &res)
	if !res.Passed || !res.Tested || len(res.Cases) != len(automod.ProbeCases) {
		t.Fatalf("test result %+v", res)
	}

	// Now it can be enabled, and the settings page is told it is tested.
	putSecondary(t, h, sign, http.StatusOK, func(m map[string]any) { point(m); m["enabled"] = true })
	if sec := secondaryOf(t, h, sign); !sec.Enabled || !sec.Tested {
		t.Fatalf("secondary after enabling: %+v", sec)
	}
	// An unrelated save with it already on is NOT refused.
	putSecondary(t, h, sign, http.StatusOK, func(m map[string]any) { m["maxCallsPerHour"] = 100 })

	// Pointing the enabled checker somewhere new needs a new test.
	putSecondary(t, h, sign, http.StatusBadRequest, func(m map[string]any) { m["endpoint"] = fake.URL + "/other" })

	// Switching it off is never refused.
	putSecondary(t, h, sign, http.StatusOK, func(m map[string]any) { m["enabled"] = false })

	// A new key forgets the pass: the key was part of what was tested.
	send(t, h, sign, http.MethodPut, "/api/v1/settings/automod-key",
		map[string]any{"checker": "secondary", "key": fakeKey("rotated")}, http.StatusOK)
	if secondaryOf(t, h, sign).Tested {
		t.Fatal("the pass survived a key change")
	}
	putSecondary(t, h, sign, http.StatusBadRequest, func(m map[string]any) { m["enabled"] = true })
}

// A Test that fails records nothing, and says which way it failed.
func TestAFailedTestRecordsNoPassAndNamesTheProblem(t *testing.T) {
	h, _, sign := sourceServer(t)
	flagsAll := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inner, _ := json.Marshal(map[string]any{"abusive": true, "confidence": 0.99, "category": "spam", "reason": "x"})
		json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{
			{"message": map[string]string{"content": string(inner)}}}})
	}))
	defer flagsAll.Close()
	var res automodTestResponse
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/automod/test", testBody(flagsAll.URL), http.StatusOK), &res)
	if res.Passed || res.Tested || res.Problem != automod.ProbeHarmlessFlagged {
		t.Fatalf("a checker that flags everything passed: %+v", res)
	}
	putSecondary(t, h, sign, http.StatusBadRequest, func(m map[string]any) {
		m["protocol"], m["preset"], m["endpoint"], m["model"] = "openai", "custom", flagsAll.URL, "fake-model"
		m["enabled"] = true
	})

	// Hostile input is refused before anything is sent.
	for _, bad := range []map[string]any{
		{"checker": "tertiary", "protocol": "openai", "endpoint": flagsAll.URL},
		{"checker": "primary", "protocol": "grpc", "endpoint": flagsAll.URL},
		{"checker": "primary", "protocol": "jev", "preset": "cerebras", "endpoint": flagsAll.URL},
		{"checker": "primary", "protocol": "openai", "endpoint": "file:///etc/passwd"},
		{"checker": "primary", "protocol": "openai", "endpoint": flagsAll.URL, "timeoutSeconds": 3600},
		{"checker": "primary", "protocol": "openai", "endpoint": flagsAll.URL, "key": "smuggled"},
	} {
		send(t, h, sign, http.MethodPost, "/api/v1/automod/test", bad, http.StatusBadRequest)
	}
}

// Saves are refused for a preset of the wrong protocol and a category table
// naming a category or action this build does not have.
func TestAPresetOrCategoryActionThisBuildDoesNotHaveIsRefused(t *testing.T) {
	h, _, sign := sourceServer(t)
	putSecondary(t, h, sign, http.StatusBadRequest, func(m map[string]any) { m["preset"] = "jev" })
	for _, table := range []map[string]any{{"spam": "teleport"}, {"flood": "delete"}} {
		var doc map[string]any
		decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK), &doc)
		doc["automod"].(map[string]any)["model"].(map[string]any)["categoryActions"] = table
		send(t, h, sign, http.MethodPut, "/api/v1/settings", doc, http.StatusBadRequest)
	}
	// And the suggested table saves.
	var doc map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK), &doc)
	doc["automod"].(map[string]any)["model"].(map[string]any)["categoryActions"] = map[string]any{
		"spam": "delete", "harassment": "timeout", "hate_speech": "timeout", "threat": "timeout"}
	send(t, h, sign, http.MethodPut, "/api/v1/settings", doc, http.StatusOK)
}

// The matrix serves the preset table, so the console's privacy labels and
// flag-only locks are the server's and cannot drift from what it enforces.
func TestTheMatrixServesThePresetTable(t *testing.T) {
	h, _, sign := sourceServer(t)
	var got struct {
		Presets         []automod.Preset   `json:"presets"`
		Protocols       []automod.Protocol `json:"protocols"`
		ModelCategories []string           `json:"modelCategories"`
	}
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/automod/matrix", nil, http.StatusOK), &got)
	if len(got.Presets) != len(automod.Presets()) || len(got.Protocols) != 2 || len(got.ModelCategories) != len(automod.ModelCategories()) {
		t.Fatalf("matrix vocabulary %+v", got)
	}
}

// DefaultSettings writes the Jev and Cerebras presets' URLs and models out as
// strings, because db cannot import automod. This holds them to the table.
func TestTheDefaultCheckerSettingsMatchThePresetTable(t *testing.T) {
	d := db.DefaultSettings().Automod
	for _, m := range []db.AutomodModel{d.Model, d.Secondary} {
		p := automod.ResolvePreset(protocolOf(m), m.Preset)
		if p.ID != m.Preset || p.Endpoint != m.Endpoint || p.Model != m.Model {
			t.Errorf("default %+v disagrees with preset %+v", m, p)
		}
	}
	if string(automod.ProtocolJev) != db.AutomodProtocolJev || string(automod.ProtocolOpenAI) != db.AutomodProtocolOpenAI {
		t.Fatal("db's protocol names drifted from automod's")
	}
	if protocolOf(d.Model) != automod.ProtocolJev {
		t.Fatal("the primary does not default to Jev")
	}
}

// A settings save rebuilds both connectors and refills NEITHER budget: each
// is the one the server was built with.
func TestApplyAutomodBuildsBothCheckersAndKeepsTheirBudgets(t *testing.T) {
	store := dbtest.OpenCheap(t)
	box, _ := secrets.New(bytes.Repeat([]byte{0x2a}, 32))
	fake, auths := smartOpenAI(t)
	store.PutAutomodKeyFor(box, db.AutomodSlotSecondary, fakeKey("sec"))
	a := db.DefaultSettings().Automod
	a.Model.Enabled, a.Model.Endpoint = true, "http://127.0.0.1:1/unreachable"
	a.Secondary.Enabled, a.Secondary.Endpoint, a.Secondary.Preset = true, fake.URL, "custom"
	pb, sb := automod.NewBudget(), automod.NewBudget()
	hub := newTestHub(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ApplyAutomod(hub, store, box, log, a, pb, sb)
	if _, err := hub.Moderator().CheckModel(t.Context(), db.PlatformTwitch, "you are worthless"); err != nil {
		t.Fatalf("the secondary did not answer for an unreachable primary: %v", err)
	}
	ApplyAutomod(hub, store, box, log, a, pb, sb)
	st := hub.Moderator().ModelStats()
	if st.Primary == nil || st.Secondary == nil || st.Primary.Protocol != automod.ProtocolJev ||
		st.Secondary.CallsThisHour != 1 || st.CallsThisHour != 1 {
		t.Fatalf("after a rebuild the budgets read %+v / %+v; want one call each carried over", st.Primary, st.Secondary)
	}
	if len(*auths) != 1 || (*auths)[0] != "Bearer "+fakeKey("sec") {
		t.Fatalf("the secondary was not sent ITS key: %v", *auths)
	}
}

// KEY LEAKS. Both keys are set and used -- through the Test button, against an
// endpoint that echoes the key back in a 401 body the way some gateways do --
// and then every surface an operator or a stranger could see is searched for
// either one: the settings API, the Test response, the server log, the audit
// events, and an exported debug bundle.
func TestNeitherModelKeyLeaksIntoSettingsResponsesLogsEventsOrTheDebugBundle(t *testing.T) {
	var logBuf bytes.Buffer
	rec := diag.NewRecorder(256, alerts.NewSecretSet(nil))
	store := dbtest.OpenCheap(t)
	if _, err := store.CreateUser("admin", testPassword); err != nil {
		t.Fatal(err)
	}
	box, _ := secrets.New(bytes.Repeat([]byte{0x2a}, 32))
	logger := slog.New(diag.NewHandler(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}), rec))
	s := New(Options{DB: store, Secrets: box, Version: "test", Log: logger, Diag: rec,
		DiagLevel: diag.NewSwitch(slog.LevelInfo), Chat: newTestHub(t)})
	var events []alerts.Event
	var evMu sync.Mutex
	s.auditSink = func(ev alerts.Event) { evMu.Lock(); events = append(events, ev); evMu.Unlock() }
	h := s.Handler()
	sign := login(t, h)

	pk, sk := fakeKey("primary"), fakeKey("secondary")
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"invalid key ` + strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") + `"}`))
	}))
	defer echo.Close()

	send(t, h, sign, http.MethodPut, "/api/v1/debug", map[string]any{"recording": true}, http.StatusOK)
	var surfaces []string
	surfaces = append(surfaces, string(send(t, h, sign, http.MethodPut, "/api/v1/settings/automod-key",
		map[string]any{"key": pk}, http.StatusOK)))
	surfaces = append(surfaces, string(send(t, h, sign, http.MethodPut, "/api/v1/settings/automod-key",
		map[string]any{"checker": "secondary", "key": sk}, http.StatusOK)))
	for _, slot := range []string{"primary", "secondary"} {
		for _, proto := range []string{"jev", "openai"} {
			b := map[string]any{"checker": slot, "protocol": proto, "endpoint": echo.URL}
			out := string(send(t, h, sign, http.MethodPost, "/api/v1/automod/test", b, http.StatusOK))
			if !strings.Contains(out, "401") {
				t.Fatalf("the test response does not report the failure: %s", out)
			}
			surfaces = append(surfaces, out)
		}
	}
	surfaces = append(surfaces, string(send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK)))
	surfaces = append(surfaces, string(send(t, h, sign, http.MethodGet, "/api/v1/automod/stats", nil, http.StatusOK)))
	// A careless future log line, to prove the bundle's scrub knows the keys
	// even though no code path today logs them.
	logger.Info("careless", "k", sk, "k2", pk)
	surfaces = append(surfaces, string(send(t, h, sign, http.MethodPost, "/api/v1/debug/export", nil, http.StatusOK)))
	evMu.Lock()
	for _, ev := range events {
		b, _ := json.Marshal(ev)
		surfaces = append(surfaces, string(b))
	}
	evMu.Unlock()
	if len(events) == 0 {
		t.Fatal("no audit events were captured; the event check checked nothing")
	}

	for i, out := range surfaces {
		if strings.Contains(out, pk) || strings.Contains(out, sk) {
			t.Errorf("surface %d carries a model key: %.300s", i, out)
		}
		if strings.Contains(out, "invalid key") {
			t.Errorf("surface %d relays the upstream error body: %.300s", i, out)
		}
	}
	// The server log is checked WITHOUT the careless line, which is there
	// only to exercise the bundle's scrub.
	logged := strings.ReplaceAll(logBuf.String(), "k="+sk+" k2="+pk, "")
	if strings.Contains(logged, pk) || strings.Contains(logged, sk) || strings.Contains(logged, "Bearer") {
		t.Errorf("the server log carries a key or an Authorization header:\n%s", logged)
	}
}

// newTestHub is a chat hub closed with the test, so its automod worker does
// not outlive it.
func newTestHub(t *testing.T) *chat.Hub {
	t.Helper()
	h := chat.New()
	t.Cleanup(h.Close)
	return h
}
