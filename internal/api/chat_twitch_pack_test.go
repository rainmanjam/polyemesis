package api

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/automod/pack"
	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// packTwitch is a Helix stub that remembers the channel's blocked terms and
// AutoMod levels, so the pack actions can be driven end to end.
type packTwitch struct {
	mu      sync.Mutex
	terms   map[string]chat.BlockedTerm // by lower-case text
	deleted []string
	next    int
	level   map[string]any
	puts    []map[string]any
	noVIPs  bool
}

func packTwitchFixture(t *testing.T) (*Server, http.Handler, func(*http.Request), *packTwitch, *db.DB) {
	t.Helper()
	s, h, store := testServer(t, config.Config{})
	pt, url := newPackTwitch(t)
	hub := chat.New()
	t.Cleanup(hub.Close)
	s.chat = hub
	attachPack(t, hub, url, "acct", "42")
	return s, h, login(t, h), pt, store
}

// attachPack connects one more Twitch account, on its own channel, to the hub.
func attachPack(t *testing.T, hub *chat.Hub, apiURL, account, broadcaster string) {
	t.Helper()
	ta, err := chat.NewTwitch(chat.TwitchConfig{
		Nick: "streamer", Channel: "streamer", AccountRef: account, Token: "tok",
		HelixToken: chat.StaticToken("helix"), ClientID: "cid",
		BroadcasterID: broadcaster, ModeratorID: broadcaster, APIBase: apiURL,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := hub.Attach(ctx, toolsAdapter{ta}); err != nil {
		t.Fatalf("Attach: %v", err)
	}
}

func newPackTwitch(t *testing.T) (*packTwitch, string) {
	t.Helper()
	pt := &packTwitch{terms: map[string]chat.BlockedTerm{}, level: map[string]any{"aggression": 1, "bullying": 0,
		"disability": 0, "misogyny": 0, "race_ethnicity_or_religion": 0, "sex_based_terms": 0,
		"sexuality_sex_or_gender": 0, "swearing": 3}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		pt.mu.Lock()
		defer pt.mu.Unlock()
		switch r.Method + " " + r.URL.Path {
		case "GET /moderation/blocked_terms":
			out := []map[string]string{}
			for _, b := range pt.terms {
				out = append(out, map[string]string{"id": b.ID, "text": b.Text, "created_at": b.CreatedAt})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": out, "pagination": map[string]string{}})
		case "POST /moderation/blocked_terms":
			text, _ := body["text"].(string)
			b, ok := pt.terms[strings.ToLower(text)]
			if !ok {
				pt.next++
				b = chat.BlockedTerm{ID: fmt.Sprintf("t%04d", pt.next), Text: text,
					CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
				pt.terms[strings.ToLower(text)] = b
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": b.ID, "text": b.Text, "created_at": b.CreatedAt}}})
		case "DELETE /moderation/blocked_terms":
			id := r.URL.Query().Get("id")
			pt.deleted = append(pt.deleted, id)
			for k, b := range pt.terms {
				if b.ID == id {
					delete(pt.terms, k)
				}
			}
			w.WriteHeader(http.StatusNoContent)
		case "GET /moderation/automod/settings":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{pt.level}})
		case "PUT /moderation/automod/settings":
			pt.puts = append(pt.puts, body)
			pt.level = body
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{body}})
		case "GET /moderation/moderators":
			io.WriteString(w, `{"data":[{"user_id":"3","user_login":"modpal","user_name":"ModPal"}]}`)
		case "GET /channels/vips":
			if pt.noVIPs {
				w.WriteHeader(http.StatusForbidden)
				io.WriteString(w, `{"error":"x","message":"vips need more"}`)
				return
			}
			io.WriteString(w, `{"data":[{"user_id":"4","user_login":"vippal","user_name":"VipPal"}]}`)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(srv.Close)
	return pt, srv.URL
}

const packTool = "/api/v1/chat/twitch/"
const packAcct = "?account=acct"

func TestPackTwitchTermsFollowTheStoredBlock(t *testing.T) {
	base, err := packTwitchTerms(db.AutomodProtection{})
	if err != nil || len(base) == 0 {
		t.Fatalf("the pack sends no terms: %d %v", len(base), err)
	}
	if len(base) > 1000 {
		t.Fatalf("%d terms is more than Twitch's 1,000-term channel limit", len(base))
	}
	seen := map[string]bool{}
	for _, term := range base {
		if seen[strings.ToLower(term)] {
			t.Fatal("a term is sent twice")
		}
		seen[strings.ToLower(term)] = true
	}

	off := false
	if got, _ := packTwitchTerms(db.AutomodProtection{Categories: map[string]db.ProtectionCategory{
		pack.CategoryHate: {Enabled: &off}}}); len(got) != 0 {
		t.Fatalf("hate & slurs is off but %d terms would be pushed", len(got))
	}
	if got, _ := packTwitchTerms(db.AutomodProtection{Categories: map[string]db.ProtectionCategory{
		pack.CategoryProfanity: {Enabled: &off}}}); len(got) != len(base) {
		t.Fatal("switching profanity off changed the hate terms")
	}

	// The operator's allow removes a term; their added severe hate term joins;
	// a profanity or contextual addition does not.
	drop := base[0]
	got, _ := packTwitchTerms(db.AutomodProtection{
		AllowTerms: []string{strings.ToUpper(drop)},
		AddTerms: []db.ProtectionTerm{
			{Term: "zzmine", Category: pack.CategoryHate, Tier: pack.TierSevere},
			{Term: "zzdefault", Category: pack.CategoryHate},
			{Term: "zzctx", Category: pack.CategoryHate, Tier: pack.TierContextual},
			{Term: "zzswear", Category: pack.CategoryProfanity, Tier: pack.TierMild},
		}})
	has := func(w string) bool {
		for _, g := range got {
			if g == w {
				return true
			}
		}
		return false
	}
	if has(drop) || !has("zzmine") || !has("zzdefault") || has("zzctx") || has("zzswear") {
		t.Fatalf("allow/add not applied: dropped-present=%v mine=%v default=%v ctx=%v swear=%v",
			has(drop), has("zzmine"), has("zzdefault"), has("zzctx"), has("zzswear"))
	}
	if len(got) != len(base)-1+2 {
		t.Fatalf("%d terms, want %d", len(got), len(base)+1)
	}
}

func TestPackSyncRecordsOnlyItsOwnTermsAndReversalRemovesOnlyThose(t *testing.T) {
	old := chat.TwitchPackBatch
	chat.TwitchPackBatch = 30
	t.Cleanup(func() { chat.TwitchPackBatch = old })
	_, h, sign, pt, store := packTwitchFixture(t)
	pt.terms["operatorword"] = chat.BlockedTerm{ID: "op1", Text: "operatorword", CreatedAt: "2020-01-01T00:00:00Z"}

	var st packStatus
	decodeInto(t, send(t, h, sign, http.MethodGet, packTool+"pack-terms"+packAcct, nil, http.StatusOK), &st)
	if st.Eligible == 0 || st.Recorded != 0 || !st.On || st.PackVersion != db.DefaultProtectionPackVersion || st.PushedPackVersion != 0 {
		t.Fatalf("status = %+v", st)
	}

	// The loop the console runs: re-post until done.
	var res chat.TrackedTermSync
	var raw []byte
	runs := 0
	for !res.Done {
		runs++
		if runs > 20 {
			t.Fatal("the sync does not converge")
		}
		raw = send(t, h, sign, http.MethodPost, packTool+"pack-terms-sync"+packAcct, map[string]any{}, http.StatusOK)
		res = chat.TrackedTermSync{}
		decodeInto(t, raw, &res)
	}
	if runs < 2 {
		t.Fatalf("%d terms fit one batch; the resumable loop is untested", st.Eligible)
	}
	set, _ := store.GetSettings()
	nat := set.Automod.Protection.Native.Twitch.Channel("42")
	if len(nat.AddedTermIDs) != st.Eligible || nat.PushedPackVersion != db.DefaultProtectionPackVersion {
		t.Fatalf("recorded %d of %d, pushed version %d", len(nat.AddedTermIDs), st.Eligible, nat.PushedPackVersion)
	}
	if _, theirs := nat.AddedTermIDs["op1"]; theirs {
		t.Fatal("the operator's own term was recorded as ours")
	}
	// The wire carries counts, never the slur list.
	terms, _ := packTwitchTerms(db.AutomodProtection{})
	for _, term := range terms[:20] {
		if strings.Contains(string(raw), term) {
			t.Fatalf("a pack term is in the sync answer")
		}
	}

	// A re-run changes nothing.
	decodeInto(t, send(t, h, sign, http.MethodPost, packTool+"pack-terms-sync"+packAcct, map[string]any{}, http.StatusOK), &res)
	if res.Added != 0 || !res.Done {
		t.Fatalf("a second sync added %d", res.Added)
	}

	// Turning hate & slurs off and syncing again reconciles the terms away.
	off := false
	if _, err := store.UpdateSettings(func(s *db.Settings) error {
		s.Automod.Protection.Categories = map[string]db.ProtectionCategory{pack.CategoryHate: {Enabled: &off}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		decodeInto(t, send(t, h, sign, http.MethodPost, packTool+"pack-terms-sync"+packAcct, map[string]any{}, http.StatusOK), &res)
		if res.Done {
			break
		}
	}
	set, _ = store.GetSettings()
	if len(set.Automod.Protection.Native.Twitch.Channel("42").AddedTermIDs) != 0 {
		t.Fatal("terms for a category that is off are still recorded")
	}
	if len(pt.terms) != 1 || pt.terms["operatorword"].ID != "op1" {
		t.Fatalf("Twitch has %d terms, want only the operator's", len(pt.terms))
	}

	// And the explicit reversal, from a fresh push.
	if _, err := store.UpdateSettings(func(s *db.Settings) error {
		s.Automod.Protection.Categories = nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		decodeInto(t, send(t, h, sign, http.MethodPost, packTool+"pack-terms-sync"+packAcct, map[string]any{}, http.StatusOK), &res)
		if res.Done {
			break
		}
	}
	decodeInto(t, send(t, h, sign, http.MethodDelete, packTool+"pack-terms"+packAcct, nil, http.StatusOK), &res)
	if !res.Done || res.Removed != st.Eligible {
		t.Fatalf("reversal = %+v", res)
	}
	set, _ = store.GetSettings()
	n := set.Automod.Protection.Native.Twitch.Channel("42")
	if len(n.AddedTermIDs) != 0 || n.PushedPackVersion != 0 || len(pt.terms) != 1 {
		t.Fatalf("after reversal: %d recorded, version %d, %d on Twitch", len(n.AddedTermIDs), n.PushedPackVersion, len(pt.terms))
	}
}

func TestPackSyncNeedsProtectionOnAndOnlyOneRunAtATime(t *testing.T) {
	s, h, sign, _, store := packTwitchFixture(t)
	if _, err := store.UpdateSettings(func(st *db.Settings) error {
		st.Automod.Protection.State = db.ProtectionOff
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	out := send(t, h, sign, http.MethodPost, packTool+"pack-terms-sync"+packAcct, map[string]any{}, http.StatusBadRequest)
	if !strings.Contains(string(out), "turn on recommended protection") {
		t.Fatalf("answer = %s", out)
	}
	if _, err := store.UpdateSettings(func(st *db.Settings) error {
		st.Automod.Protection.State = db.ProtectionOn
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s.packMu.Lock()
	defer s.packMu.Unlock()
	for _, m := range []string{http.MethodPost, http.MethodDelete} {
		path := packTool + "pack-terms" + packAcct
		var body any
		if m == http.MethodPost {
			path, body = packTool+"pack-terms-sync"+packAcct, map[string]any{}
		}
		out := send(t, h, sign, m, path, body, http.StatusConflict)
		if !strings.Contains(string(out), "already running") {
			t.Fatalf("%s answer = %s", m, out)
		}
	}
}

func TestPackAutomodLevelRaisesAndKeepsSwearing(t *testing.T) {
	_, h, sign, pt, _ := packTwitchFixture(t)
	var got packLevelAnswer
	decodeInto(t, send(t, h, sign, http.MethodPut, packTool+"pack-automod-level"+packAcct, map[string]any{"level": 2}, http.StatusOK), &got)
	if got.Before.Aggression != 1 || got.After.Aggression != 2 || got.After.Bullying != 2 || got.After.Swearing != 3 {
		t.Fatalf("answer = %+v", got)
	}
	if len(pt.puts) != 1 || pt.puts[0]["swearing"] != float64(3) {
		t.Fatalf("puts = %v", pt.puts)
	}
	if _, has := pt.puts[0]["overall_level"]; has {
		t.Fatal("overall_level resets swearing")
	}
	out := send(t, h, sign, http.MethodPut, packTool+"pack-automod-level"+packAcct, map[string]any{"level": 4}, http.StatusBadRequest)
	if !strings.Contains(string(out), "1 or 2") {
		t.Fatalf("answer = %s", out)
	}
}

func TestPackProtectedNamesLearnsModeratorsAndVIPs(t *testing.T) {
	s, h, sign, pt, _ := packTwitchFixture(t)
	before := s.automodState.Names.Len()
	var got packNamesAnswer
	decodeInto(t, send(t, h, sign, http.MethodPost, packTool+"pack-protected-names"+packAcct, map[string]any{}, http.StatusOK), &got)
	if got.Moderators != 1 || got.VIPs != 1 || got.VIPNote != "" {
		t.Fatalf("answer = %+v", got)
	}
	// Name and login both: ModPal / modpal fold to one name, VipPal likewise.
	if grew := s.automodState.Names.Len() - before; grew != 2 {
		t.Fatalf("learned %d names, want 2", grew)
	}

	// VIPs failing is a note, not a failure; the moderators still count.
	pt.noVIPs = true
	decodeInto(t, send(t, h, sign, http.MethodPost, packTool+"pack-protected-names"+packAcct, map[string]any{}, http.StatusOK), &got)
	if got.Moderators != 1 || got.VIPs != 0 || got.VIPNote == "" {
		t.Fatalf("answer = %+v", got)
	}

	// With no persistent state wired, nothing panics and the counts still read.
	s.automodState = nil
	decodeInto(t, send(t, h, sign, http.MethodPost, packTool+"pack-protected-names"+packAcct, map[string]any{}, http.StatusOK), &got)
	if got.Moderators != 1 {
		t.Fatalf("answer = %+v", got)
	}
}

func TestPackToolsFailPlainlyWhenTwitchIsNotConnected(t *testing.T) {
	s, h, store := testServer(t, config.Config{})
	_ = store
	hub := chat.New()
	t.Cleanup(hub.Close)
	s.chat = hub
	sign := login(t, h)
	send(t, h, sign, http.MethodGet, packTool+"pack-terms"+packAcct, nil, http.StatusBadGateway)
	out := send(t, h, sign, http.MethodPost, packTool+"pack-terms-sync"+packAcct, map[string]any{}, http.StatusBadRequest)
	if !strings.Contains(string(out), "twitch is not connected") {
		t.Fatalf("answer = %s", out)
	}
}

// The term record belongs to the server: a settings save from a stale or
// hostile page can neither plant nor wipe it, and recording a term does not
// make an open settings page stale.
func TestNativeRecordIsServerOwnedAndOutsideTheSettingsVersion(t *testing.T) {
	h, store, sign := sourceServer(t)
	if _, err := store.UpdateSettings(func(s *db.Settings) error {
		s.Automod.Protection.Native.Twitch.Put(db.ProtectionTwitchChannel{
			BroadcasterID: "42", AddedTermIDs: map[string]string{"keep1": "aa"}, PushedPackVersion: 1})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var cur settingsResponse
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK), &cur)

	var doc map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK), &doc)
	prot := doc["automod"].(map[string]any)["protection"].(map[string]any)
	prot["native"] = map[string]any{"twitch": map[string]any{"channels": []any{map[string]any{
		"broadcasterId": "42", "addedTermIds": map[string]any{"planted": "zz"}, "pushedPackVersion": 9}}}}
	doc["version"] = cur.Version
	send(t, h, sign, http.MethodPut, "/api/v1/settings", doc, http.StatusOK)
	s, _ := store.GetSettings()
	n := s.Automod.Protection.Native.Twitch.Channel("42")
	if len(n.AddedTermIDs) != 1 || n.AddedTermIDs["keep1"] != "aa" || n.PushedPackVersion != 1 {
		t.Fatalf("a settings save changed the term record: %+v", n)
	}

	// Recording more terms leaves the version alone, so a page opened before
	// the sync can still save.
	if _, err := store.UpdateSettings(func(s *db.Settings) error {
		c := s.Automod.Protection.Native.Twitch.Channel("42")
		c.AddedTermIDs["more"] = "bb"
		s.Automod.Protection.Native.Twitch.Put(c)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var after settingsResponse
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK), &after)
	if after.Version != cur.Version {
		t.Fatal("recording Twitch term ids changed the settings version")
	}
}

func TestProtectionNativeProblems(t *testing.T) {
	put := func(c ...db.ProtectionTwitchChannel) db.Settings {
		s := db.DefaultSettings()
		s.Automod.Protection.Native.Twitch.Channels = c
		return s
	}
	if err := put(db.ProtectionTwitchChannel{BroadcasterID: "1", PushedPackVersion: -1}).Validate(); err == nil {
		t.Fatal("a negative pushed version was accepted")
	}
	big := map[string]string{}
	for i := 0; i <= db.MaxNativeTermIDs; i++ {
		big[fmt.Sprint(i)] = "x"
	}
	if err := put(db.ProtectionTwitchChannel{BroadcasterID: "1", AddedTermIDs: big}).Validate(); err == nil {
		t.Fatal("an oversized record was accepted")
	}
	if err := put(db.ProtectionTwitchChannel{}).Validate(); err == nil {
		t.Fatal("a record with no channel was accepted")
	}
	if err := put(db.ProtectionTwitchChannel{BroadcasterID: "1"}, db.ProtectionTwitchChannel{BroadcasterID: "1"}).Validate(); err == nil {
		t.Fatal("two records for one channel were accepted")
	}
}

// One record per channel: syncing channel B must not forget what channel A has.
func TestPackRecordIsPerChannel(t *testing.T) {
	old := chat.TwitchPackBatch
	chat.TwitchPackBatch = 1000
	t.Cleanup(func() { chat.TwitchPackBatch = old })
	s, h, sign, ptA, store := packTwitchFixture(t)
	ptB, urlB := newPackTwitch(t)
	attachPack(t, s.chat, urlB, "acctB", "77")

	syncAll := func(acct string) {
		var res chat.TrackedTermSync
		for i := 0; i < 20 && !res.Done; i++ {
			res = chat.TrackedTermSync{}
			decodeInto(t, send(t, h, sign, http.MethodPost, packTool+"pack-terms-sync?account="+acct, map[string]any{}, http.StatusOK), &res)
		}
		if !res.Done {
			t.Fatal("sync did not finish")
		}
	}
	syncAll("acct")
	syncAll("acctB")

	set, _ := store.GetSettings()
	nat := set.Automod.Protection.Native.Twitch
	a, b := nat.Channel("42"), nat.Channel("77")
	if len(nat.Channels) != 2 || len(a.AddedTermIDs) == 0 || len(a.AddedTermIDs) != len(b.AddedTermIDs) {
		t.Fatalf("records: A=%d B=%d channels=%d", len(a.AddedTermIDs), len(b.AddedTermIDs), len(nat.Channels))
	}
	if len(ptA.deleted) != 0 || len(ptB.deleted) != 0 {
		t.Fatalf("syncing B deleted terms: A=%v B=%v", ptA.deleted, ptB.deleted)
	}
	for _, acct := range []string{"acct", "acctB"} {
		var st packStatus
		decodeInto(t, send(t, h, sign, http.MethodGet, packTool+"pack-terms?account="+acct, nil, http.StatusOK), &st)
		if st.Recorded != len(a.AddedTermIDs) {
			t.Fatalf("%s status recorded %d", acct, st.Recorded)
		}
	}

	// Removing B's terms leaves A's record and A's terms alone.
	send(t, h, sign, http.MethodDelete, packTool+"pack-terms?account=acctB", nil, http.StatusOK)
	set, _ = store.GetSettings()
	nat = set.Automod.Protection.Native.Twitch
	if len(nat.Channel("42").AddedTermIDs) != len(a.AddedTermIDs) || len(nat.Channel("77").AddedTermIDs) != 0 {
		t.Fatalf("after removing B: A=%d B=%d", len(nat.Channel("42").AddedTermIDs), len(nat.Channel("77").AddedTermIDs))
	}
	if len(ptA.terms) != len(a.AddedTermIDs) || len(ptB.terms) != 0 {
		t.Fatalf("Twitch has A=%d B=%d terms", len(ptA.terms), len(ptB.terms))
	}
}

// The stored document, and so GET /settings, never holds a pack term.
func TestPackRecordHoldsNoTerm(t *testing.T) {
	old := chat.TwitchPackBatch
	chat.TwitchPackBatch = 1000
	t.Cleanup(func() { chat.TwitchPackBatch = old })
	_, h, sign, _, store := packTwitchFixture(t)
	var res chat.TrackedTermSync
	for i := 0; i < 20 && !res.Done; i++ {
		decodeInto(t, send(t, h, sign, http.MethodPost, packTool+"pack-terms-sync"+packAcct, map[string]any{}, http.StatusOK), &res)
	}
	set, _ := store.GetSettings()
	if len(set.Automod.Protection.Native.Twitch.Channel("42").AddedTermIDs) == 0 {
		t.Fatal("nothing recorded")
	}
	out := string(send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK))
	terms, _ := packTwitchTerms(db.AutomodProtection{})
	for _, term := range terms {
		// A long pack term cannot occur in the hex hashes by accident.
		if len(term) >= 5 && strings.Contains(out, "\""+term+"\"") {
			t.Fatalf("GET /settings carries the pack term %q", term)
		}
	}
	for _, v := range set.Automod.Protection.Native.Twitch.Channel("42").AddedTermIDs {
		if _, err := hex.DecodeString(v); err != nil || len(v) != 16 {
			t.Fatalf("recorded value %q is not a term hash", v)
		}
	}
}

// Recording writes only the record: a stored document that fails validation
// for an unrelated reason must not make polyemesis forget terms Twitch took.
func TestPackRecordSurvivesAnInvalidStoredDocument(t *testing.T) {
	_, h, sign, pt, store := packTwitchFixture(t)
	st, _ := store.GetSettings()
	st.Automod.Protection.LadderTimeoutSeconds = -5 // fails Validate()
	if err := st.Validate(); err == nil {
		t.Fatal("the premise is gone: this document must fail validation")
	}
	if err := store.PutSettings(st); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		var res chat.TrackedTermSync
		decodeInto(t, send(t, h, sign, http.MethodPost, packTool+"pack-terms-sync"+packAcct, map[string]any{}, http.StatusOK), &res)
		if res.Done {
			break
		}
	}
	got, _ := store.GetSettings()
	if n := len(got.Automod.Protection.Native.Twitch.Channel("42").AddedTermIDs); n == 0 || n != len(pt.terms) {
		t.Fatalf("recorded %d of the %d terms Twitch accepted", n, len(pt.terms))
	}
}

// When the record cannot be written, the answer says the terms are on Twitch
// and untracked, not just a store error.
func TestPackRecordFailureSaysTheTermsAreUntracked(t *testing.T) {
	s, _, _, _, store := packTwitchFixture(t)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	err := s.recordNative("42", chat.TrackedTermSync{AddedIDs: map[string]string{"a": "k", "b": "k2"}}, false)
	if err == nil || !strings.Contains(err.Error(), "2 terms were added to Twitch but could not be recorded") {
		t.Fatalf("err = %v", err)
	}
}
