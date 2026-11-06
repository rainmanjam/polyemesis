package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/secrets"
)

// The playback token is a bearer credential. playout.json is a sidecar the
// operator backs up and may paste into a ticket, so it holds the token sealed
// with the same key the database seals stream keys with, never in the clear.
func TestPlayoutConfigDoesNotStoreTheTokenInTheClear(t *testing.T) {
	dir := t.TempDir()
	s, _, _ := testServer(t, config.Config{DataDir: dir})

	saved, err := s.playoutStore().save(playoutPublish{Protection: PlayoutProtectToken, Title: "Studio"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(s.playoutConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if saved.Token == "" || bytes.Contains(raw, []byte(saved.Token)) {
		t.Fatalf("playout.json holds the playback token in the clear:\n%s", raw)
	}
	if !strings.Contains(string(raw), `"Studio"`) {
		t.Errorf("the rest of the config should stay readable:\n%s", raw)
	}

	// A fresh process (a new store over the same file) recovers the same token.
	got := (&playoutStore{path: s.playoutConfigPath(), box: s.box, log: s.log}).load()
	if got.Token != saved.Token || got.Title != "Studio" {
		t.Errorf("reloaded %+v, want token %q and title Studio", got, saved.Token)
	}
}

// An install upgraded from a plaintext playout.json keeps its token (existing
// links keep working) and the file is rewritten sealed.
func TestPlayoutConfigMigratesAPlaintextTokenInPlace(t *testing.T) {
	dir := t.TempDir()
	s, _, _ := testServer(t, config.Config{DataDir: dir})
	legacy := `{"protection":"token","token":"legacy-token-value","title":"Old"}`
	if err := os.WriteFile(s.playoutConfigPath(), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	got := (&playoutStore{path: s.playoutConfigPath(), box: s.box, log: s.log}).load()
	if got.Token != "legacy-token-value" || got.Title != "Old" {
		t.Fatalf("legacy file read as %+v", got)
	}
	raw, _ := os.ReadFile(s.playoutConfigPath())
	if bytes.Contains(raw, []byte("legacy-token-value")) {
		t.Errorf("the plaintext token was left in playout.json:\n%s", raw)
	}
	var disk map[string]any
	if err := json.Unmarshal(raw, &disk); err != nil || disk["tokenEnc"] == nil {
		t.Errorf("rewritten file has no tokenEnc: %v\n%s", err, raw)
	}
}

// A file that cannot be read is replaced with the protected default, and the
// operator is told, because the title, offline card and token are gone.
func TestPlayoutConfigSaysWhenItReplacesACorruptFile(t *testing.T) {
	dir := t.TempDir()
	s, _, _ := testServer(t, config.Config{DataDir: dir})
	if err := os.WriteFile(s.playoutConfigPath(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))

	got := (&playoutStore{path: s.playoutConfigPath(), box: s.box, log: log}).load()
	if got.Protection != PlayoutProtectToken || got.Token == "" {
		t.Errorf("a corrupt file must fall back to the protected default, got %+v", got)
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "playout.json") {
		t.Errorf("no WARN naming playout.json:\n%s", logs.String())
	}
}

// A sealed token that this install's key cannot open (a restored backup without
// its secret.key) keeps the rest of the config, mints a new token and says so.
func TestPlayoutConfigWithAnUnopenableTokenMintsANewOne(t *testing.T) {
	dir := t.TempDir()
	s, _, _ := testServer(t, config.Config{DataDir: dir})
	saved, err := s.playoutStore().save(playoutPublish{Protection: PlayoutProtectToken, Title: "Keep me"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := secrets.New(bytes.Repeat([]byte{0x07}, 32))
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	got := (&playoutStore{path: s.playoutConfigPath(), box: other, log: slog.New(slog.NewTextHandler(&logs, nil))}).load()
	if got.Token == "" || got.Token == saved.Token || got.Title != "Keep me" {
		t.Errorf("got %+v: want a fresh token and the title kept", got)
	}
	if !strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("no WARN for the lost token:\n%s", logs.String())
	}
}

// An upgraded install that nobody visits still gets its plaintext token sealed:
// building the server migrates the sidecar, no request needed.
func TestNewMigratesAPlaintextPlayoutTokenAtStartup(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/playout.json"
	if err := os.WriteFile(path, []byte(`{"protection":"token","token":"legacy-startup-token"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	testServer(t, config.Config{DataDir: dir})

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("legacy-startup-token")) || !bytes.Contains(raw, []byte("tokenEnc")) {
		t.Errorf("the token was not sealed at startup:\n%s", raw)
	}
}
