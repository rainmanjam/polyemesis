package db

// #306, part one: STOP THE DIVERGENCE.
//
// A destination's stream key was configured with a bracketed-paste artefact
// glued onto it -- the real key followed by ESC [ 2 7 ; 2 ; 1 3 -- so 65 bytes
// were stored. FFmpeg stopped reading the publish URL at the ESC, opened the
// 56-byte prefix, failed, and printed the prefix back onto stderr, where
// supervisor wrote it to data/logs/process.log. The credential scrubber holds
// EXACT literals and it held the stored 65 bytes, so it matched nothing.
//
// Every downstream defence in this repository -- alerts.SecretSet, the API leak
// scan in internal/api/argv_leak_test.go, step 8 of
// scripts/acceptance-multistream.sh -- assumes the value it was told about is
// the value that goes out. These tests pin the boundary that makes the
// assumption true.
//
// The refusal is deliberately not a repair; the reason is written out in full
// beside the check in destinations.go.

import (
	"strings"
	"testing"
)

// pasteArtefact is the exact byte sequence off the run in #306.
const pasteArtefact = "\x1b[27;2;13"

// A synthetic key, and named `sentinel` rather than `key` because gitleaks
// matches on the identifier and a finding in the working tree disables the
// allowlist self-test.
const sentinelKey = "SENTINEL-db-control-1d8f5b02e4"

// goodDest is db_test.go's validDest with the sentinel key on it, so a test
// that adds one bad byte is measuring that byte and nothing else.
func goodDest() *Destination {
	d := validDest()
	d.StreamKey = sentinelKey
	return d
}

// TestAStreamKeyWithAControlCharacterIsRefused is the whole of part one.
//
// The placements are listed separately because they fail differently
// downstream: a trailing artefact truncates the key, a leading one replaces it,
// and an interior one splits it. All three produce a stored value that is not
// the value FFmpeg opens, which is the property being refused.
func TestAStreamKeyWithAControlCharacterIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
	}{
		{"paste artefact after the key", sentinelKey + pasteArtefact},
		{"paste artefact before the key", pasteArtefact + sentinelKey},
		{"escape in the middle", sentinelKey[:12] + "\x1b" + sentinelKey[12:]},
		{"a stray newline", sentinelKey + "\n"},
		{"a stray tab", sentinelKey + "\t"},
		{"a NUL", sentinelKey + "\x00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := goodDest()
			d.StreamKey = tc.value
			err := d.Validate()
			if err == nil {
				t.Fatalf("Validate accepted a stream key carrying a control character.\n" +
					"FFmpeg stops reading the publish URL there, so the stored key and the " +
					"key that reaches the platform are different strings -- and " +
					"alerts.SecretSet, which is exact substring replacement, only knows the " +
					"stored one. That is #306: the key was written to process.log in the " +
					"clear on a live run with the scrub correctly wired.")
			}
			if !strings.Contains(err.Error(), "stream key contains a control character") {
				t.Errorf("the refusal does not say what is wrong, so an operator staring at a "+
					"field that looks correct has nothing to go on: %v", err)
			}

			// THE MESSAGE MUST NOT ECHO THE KEY. It is rendered into a 400 body
			// and into the server log; a diagnostic that quotes the credential
			// to complain about it is the same disclosure by a shorter route.
			if strings.Contains(err.Error(), sentinelKey[:12]) {
				t.Errorf("the validation error quotes the stream key back: %v", err)
			}
		})
	}
}

// The backup key is a separate field on the same row, and it is the one that
// carries the show when the primary drops. destSecrets covers both for exactly
// this reason; so must the boundary.
func TestABackupStreamKeyWithAControlCharacterIsRefused(t *testing.T) {
	d := goodDest()
	d.BackupStreamKey = sentinelKey + pasteArtefact
	err := d.Validate()
	if err == nil {
		t.Fatal("Validate accepted a BACKUP stream key carrying a control character. A fix " +
			"applied to the primary only leaves dest:N:backup leaking on exactly the route " +
			"dest:N no longer does.")
	}
	if !strings.Contains(err.Error(), "backup stream key contains a control character") {
		t.Errorf("the refusal names the wrong field: %v", err)
	}
}

// The refusal must reach the store, not just the model: CreateDestination and
// UpdateDestination are what the API calls, and internal/api turns their error
// into a 400.
func TestCreateDestinationRefusesAKeyWithAControlCharacter(t *testing.T) {
	d := testDB(t)

	row := goodDest()
	row.StreamKey = sentinelKey + pasteArtefact
	if _, err := d.CreateDestination(row); err == nil {
		t.Fatal("CreateDestination stored a key with a control character in it. The row " +
			"would publish with a truncated key and leak the truncation into process.log.")
	}

	// And the ordinary key still goes in, or this check has simply broken
	// destinations. A refusal that refuses everything is not a refusal.
	good := goodDest()
	created, err := d.CreateDestination(good)
	if err != nil {
		t.Fatalf("CreateDestination refused an ordinary key: %v", err)
	}
	if created.StreamKey != sentinelKey {
		t.Errorf("stream key = %q, want it stored byte for byte", created.StreamKey)
	}

	created.StreamKey = sentinelKey + pasteArtefact
	if _, err := d.UpdateDestination(created); err == nil {
		t.Fatal("UpdateDestination stored a key with a control character in it")
	}
}
