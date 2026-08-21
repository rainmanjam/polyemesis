package db

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Poka-yoke audit #10, and the "sixth path" behind GHSA-7jqx-76vq-hvfc: five
// distinct routes were found by which a destination's stream key escaped
// sealing, all because sealing is done by hand-written per-field helpers
// (sealStreamKey/openStreamKey in destinations.go) rather than by the schema.
// Every OTHER secret-bearing table in this database already makes plaintext
// storage impossible -- mqtt_creds.password_enc, automod_creds.key_enc,
// platform_creds.client_secret_enc, platform_accounts.access_token_enc/
// refresh_token_enc and hooks.secret are all `BLOB NOT NULL`, so there is no
// column a bug could write an unsealed credential into. destinations is the
// one table that still permits it: stream_key/backup_stream_key are TEXT,
// kept plaintext-capable for the length of one upgrade (see the comment on
// destinations.stream_key in schema.sql) with the sealed *_enc BLOB sibling
// carrying the ciphertext once a key is configured.
//
// This test does not, and cannot, close that one deliberate exception -- doing
// so would break the upgrade fallback openStreamKey depends on. What it closes
// is a NEW one: a column named like a credential (key/secret/password/token/
// credential), added anywhere in this package -- CREATE TABLE in schema.sql
// for a fresh install, or an `ALTER TABLE ... ADD COLUMN` string for an
// upgrade -- fails this test unless it is sealed (BLOB), structurally not a
// credential value (INTEGER, or a `_hash` column), paired with a sibling
// `<name>_enc BLOB` column the way stream_key is, or named in the exceptions
// map below with a reason. Control is out of reach here without rewriting the
// dual-column upgrade fallback; this is the Warning that stands in for it --
// it fails loudly at `go test ./...` on the commit that adds the column,
// rather than relying on a reviewer remembering the convention.
func TestNoNewSecretColumnEscapesSealing(t *testing.T) {
	secretName := regexp.MustCompile(`(?i)(key|secret|password|token|credential)`)

	// table.column -> why it is allowed to be a plain, unsealed column despite
	// matching the name pattern above. Every entry here is a decision, not an
	// oversight; a name absent from both this map and the structural rules in
	// the loop below is a column with no sealing story at all.
	exceptions := map[string]string{
		"sources.token": "per-source publish secret, deliberately plaintext: an " +
			"ingest token is pasted into OBS and the operator has to be able to " +
			"read it back (schema.sql, sources table comment)",
		"sources.prev_token": "the token sources.token rotated away from, kept " +
			"readable for the same reason during its grace window",
	}

	cols, encSiblings := loadSecretColumnCandidates(t, secretName)

	for table, byCol := range cols {
		for col, sqlType := range byCol {
			key := table + "." + col

			switch {
			case sqlType == "BLOB":
				continue // sealed at rest; nothing to check
			case sqlType == "INTEGER":
				continue // a counter/epoch/version, not a credential value
			case strings.HasSuffix(col, "_hash"):
				continue // irreversible; the plaintext was never stored
			case encSiblings[table][col+"_enc"]:
				continue // stream_key's pattern: sealed sibling carries the real value
			}

			if _, ok := exceptions[key]; ok {
				continue
			}

			t.Errorf("%s is a plain %s column whose name matches a credential "+
				"(key/secret/password/token/credential) with no sealed *_enc "+
				"sibling and no documented exception. Either seal it (a BLOB "+
				"column plus Seal/Open helpers, the mqtt_creds/hooks pattern), "+
				"give it a sibling %s_enc BLOB column and route writes through "+
				"it (the destinations.stream_key pattern), or add %q to the "+
				"exceptions map above with a reason. This is the sixth-path "+
				"guard for GHSA-7jqx-76vq-hvfc: destinations is the only table "+
				"today where sealing is enforced by convention instead of the "+
				"schema, and this test is what stops that becoming two.",
				key, sqlType, col, key)
		}
	}
}

// loadSecretColumnCandidates scans schema.sql's CREATE TABLE statements (what
// a fresh install gets) and every `ALTER TABLE ... ADD COLUMN` string literal
// in this package's non-test .go files (what an upgrade gets) for columns
// whose name matches secretName. destinations.backup_stream_key_enc, for
// example, exists only as an ALTER TABLE in destinations.go -- schema.sql
// alone would have missed it, and so would have missed the very sibling this
// test relies on to excuse stream_key.
//
// It also returns, per table, the full set of _enc BLOB column names, so the
// caller can check the sibling-pairing rule without a second pass.
func loadSecretColumnCandidates(t *testing.T, secretName *regexp.Regexp) (cols map[string]map[string]string, encSiblings map[string]map[string]bool) {
	t.Helper()
	cols = map[string]map[string]string{}
	encSiblings = map[string]map[string]bool{}

	record := func(table, col, sqlType string) {
		if sqlType == "BLOB" {
			if encSiblings[table] == nil {
				encSiblings[table] = map[string]bool{}
			}
			encSiblings[table][col] = true
		}
		if !secretName.MatchString(col) {
			return
		}
		if cols[table] == nil {
			cols[table] = map[string]string{}
		}
		cols[table][col] = sqlType
	}

	// CREATE TABLE bodies in schema.sql.
	schemaPath := filepath.Join("schema.sql")
	raw, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("read schema.sql: %v", err)
	}
	tableRe := regexp.MustCompile(`(?s)CREATE TABLE IF NOT EXISTS (\w+) \((.*?)\n\);`)
	colRe := regexp.MustCompile(`(?m)^\s*(\w+)\s+(BLOB|TEXT|INTEGER|REAL)\b`)
	for _, tm := range tableRe.FindAllStringSubmatch(string(raw), -1) {
		table, body := tm[1], tm[2]
		for _, cm := range colRe.FindAllStringSubmatch(body, -1) {
			record(table, cm[1], cm[2])
		}
	}

	// ALTER TABLE ... ADD COLUMN in every non-test .go file in this package --
	// the upgrade path, which is where backup_stream_key_enc and every
	// tr_/au_/rs_ destinations column actually live.
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read internal/db: %v", err)
	}
	alterRe := regexp.MustCompile(`ALTER TABLE (\w+) ADD COLUMN (\w+) (BLOB|TEXT|INTEGER|REAL)`)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, am := range alterRe.FindAllStringSubmatch(string(src), -1) {
			record(am[1], am[2], am[3])
		}
	}

	return cols, encSiblings
}
