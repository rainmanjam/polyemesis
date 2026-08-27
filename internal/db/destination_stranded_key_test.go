package db

// #610: A STREAM KEY ON A NON-RTMP DESTINATION IS STORED, NEVER SENT, AND
// UNREACHABLE.
//
// Retyping an existing RTMP destination to srt, file or audio left the key on
// the row. Target() joins a key on for RTMP and only for RTMP, so the
// destination went on publishing to the bare URL with no credential; the dialog
// renders a key field only for RTMP, so no screen showed it again; and GET
// /destinations went on returning it in full. Misconfigured AND un-rotatable,
// with nothing anywhere saying either.
//
// Two rungs, and this file pins both:
//
//   - CONTROL. Validate refuses the combination, naming the kind.
//   - THE ROWS THAT ALREADY HAVE IT. MigrateStrandedStreamKeys clears them at
//     Open, so the new refusal cannot land on a destination the operator has no
//     way to fix -- the API decodes an update body over the row it just read, so
//     the stranded key would come back round with every save.
//
// NO TEST HERE PRINTS A KEY. The sentinel below is synthetic, and an assertion
// that echoed it would put a credential-shaped string in CI output for a file
// whose entire subject is credentials that escaped.

import (
	"path/filepath"
	"strings"
	"testing"
)

// strandedSentinel is synthetic, and named `sentinel` rather than `key` because
// gitleaks matches on the identifier -- see destination_key_control_test.go.
const strandedSentinel = "SENTINEL-db-stranded-6c41e97b2a"

// keylessKinds are every kind whose publish URL has nowhere to put a key, with
// a URL each that passes the rest of Validate, so a failure here is about the
// key and nothing else.
var keylessKinds = []struct {
	kind DestKind
	url  string
}{
	{DestSRT, "srt://a.example:9000"},
	{DestFile, "recording.mkv"},
	{DestAudio, "icecast://user:pass@a.example:8000/live.mp3"},
}

// TestAStreamKeyOnAKindThatCannotCarryOneIsRefused is the Control rung.
//
// The message has to NAME THE KIND. "invalid destination" on a form whose key
// field is not even rendered for the kind it is complaining about is a dead
// end; the kind is the thing the operator changed and the thing they can change
// back.
func TestAStreamKeyOnAKindThatCannotCarryOneIsRefused(t *testing.T) {
	for _, tc := range keylessKinds {
		t.Run(string(tc.kind), func(t *testing.T) {
			d := validDest()
			d.Kind = tc.kind
			d.URL = tc.url
			d.StreamKey = strandedSentinel

			err := d.Validate()
			if err == nil {
				t.Fatalf("a %s destination carrying a stream key was accepted", tc.kind)
			}
			if !strings.Contains(err.Error(), string(tc.kind)) {
				t.Errorf("the refusal does not name the kind %q, so it names nothing the "+
					"operator can act on: %v", tc.kind, err)
			}
			if strings.Contains(err.Error(), strandedSentinel) {
				// Not a style point. A validation error is rendered into a 400
				// body and into the server log.
				t.Errorf("the refusal echoed the stream key")
			}
		})
	}
}

// TestAnRTMPDestinationWithAStreamKeyStillSaves is the control for the refusal
// above: the ordinary configuration, which is most of them, must be untouched.
func TestAnRTMPDestinationWithAStreamKeyStillSaves(t *testing.T) {
	d := testDB(t)

	dst := validDest()
	dst.StreamKey = strandedSentinel
	saved, err := d.CreateDestination(dst)
	if err != nil {
		t.Fatalf("an RTMP destination with a stream key was refused: %v", err)
	}
	if saved.StreamKey != strandedSentinel {
		t.Fatalf("the stream key of an RTMP destination did not survive the save")
	}
	if saved.Target() == saved.URL {
		t.Errorf("Target() did not join the key onto the URL")
	}

	saved.Name = "Renamed"
	again, err := d.UpdateDestination(saved)
	if err != nil {
		t.Fatalf("updating an RTMP destination with a stream key: %v", err)
	}
	if again.StreamKey != strandedSentinel {
		t.Errorf("the stream key of an RTMP destination did not survive an update")
	}
}

// TestRetypingADestinationAwayFromRTMPWithItsKeyIsRefused is the bug's own
// route: the update path, which is where the operator produces the state.
func TestRetypingADestinationAwayFromRTMPWithItsKeyIsRefused(t *testing.T) {
	for _, tc := range keylessKinds {
		t.Run(string(tc.kind), func(t *testing.T) {
			d := testDB(t)

			dst := validDest()
			dst.StreamKey = strandedSentinel
			saved, err := d.CreateDestination(dst)
			if err != nil {
				t.Fatalf("create: %v", err)
			}

			// Exactly what the dialog used to send: a new kind and a new URL,
			// with the key left where it was.
			saved.Kind = tc.kind
			saved.URL = tc.url
			if _, err := d.UpdateDestination(saved); err == nil {
				t.Fatalf("retyping an RTMP destination to %s with its key still on it was "+
					"accepted, so it publishes with no credential and no screen shows the "+
					"key again", tc.kind)
			}

			// And the row is untouched, which is what makes the refusal safe:
			// a rejected save must not half-apply.
			back, err := d.GetDestination(saved.ID)
			if err != nil {
				t.Fatalf("read back: %v", err)
			}
			if back.Kind != DestRTMP || back.StreamKey != strandedSentinel {
				t.Errorf("the refused update changed the row anyway: kind is now %q", back.Kind)
			}
		})
	}
}

// strandRow retypes a destination behind Validate's back, which is the only way
// to build the state an upgrading install arrives with -- the release that
// produced it is not the one running the test.
func strandRow(t *testing.T, d *DB, id int64, kind DestKind, url string) {
	t.Helper()
	if _, err := d.sql.Exec(`UPDATE destinations SET kind=?, url=? WHERE id=?`,
		string(kind), url, id); err != nil {
		t.Fatalf("strand destination %d: %v", id, err)
	}
}

// TestAStrandedStreamKeyIsClearedOnOpen is the half that keeps the refusal from
// stranding the OPERATOR instead of the key.
//
// Without it, an install upgrading into this release has rows Validate now
// refuses, UpdateDestination validates before it writes, and the API builds the
// struct it validates by decoding the request body over the row it just read.
// So the key comes back round with every save and the destination cannot be
// renamed, disabled or repointed -- and the dialog renders no key field for
// these kinds, so there is nothing on screen to clear.
func TestAStrandedStreamKeyIsClearedOnOpen(t *testing.T) {
	for _, tc := range keylessKinds {
		t.Run(string(tc.kind), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "polyemesis.db")

			first := keyDB(t, path)
			dst := validDest()
			dst.StreamKey = strandedSentinel
			saved, err := first.CreateDestination(dst)
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			strandRow(t, first, saved.ID, tc.kind, tc.url)
			if err := first.Close(); err != nil {
				t.Fatalf("close: %v", err)
			}

			second := keyDB(t, path)
			back, err := second.GetDestination(saved.ID)
			if err != nil {
				t.Fatalf("read back: %v", err)
			}
			if back.StreamKey != "" {
				t.Fatalf("the stranded stream key survived Open on a %s destination", tc.kind)
			}

			// The columns, not just what the reader hands back: a key left in
			// either one is still a credential in the database file.
			var plain string
			var enc []byte
			if err := second.sql.QueryRow(
				`SELECT stream_key, stream_key_enc FROM destinations WHERE id=?`,
				saved.ID).Scan(&plain, &enc); err != nil {
				t.Fatalf("read columns: %v", err)
			}
			if plain != "" {
				t.Errorf("the plaintext stream_key column still holds a key")
			}
			if len(enc) != 0 {
				t.Errorf("the sealed stream_key_enc column still holds a key")
			}

			// The point of clearing it: the destination is editable again.
			back.Name = "Renamed after the upgrade"
			if _, err := second.UpdateDestination(back); err != nil {
				t.Fatalf("an upgraded %s destination could not be renamed, which is the "+
					"operator being stranded instead of the key: %v", tc.kind, err)
			}
		})
	}
}

// TestOpenLeavesAnRTMPDestinationsStreamKeyAlone is the control for the sweep.
// It clears keys that cannot be sent; a sweep that also cleared the ones that
// can would take every install offline on upgrade.
func TestOpenLeavesAnRTMPDestinationsStreamKeyAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polyemesis.db")

	first := keyDB(t, path)
	dst := validDest()
	dst.StreamKey = strandedSentinel
	saved, err := first.CreateDestination(dst)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second := keyDB(t, path)
	back, err := second.GetDestination(saved.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if back.StreamKey != strandedSentinel {
		t.Errorf("the stream key of an RTMP destination did not survive Open")
	}
}

// TestASealedStrandedStreamKeyIsClearedAndASealedRTMPOneSurvives runs the sweep
// against the encrypted columns, where the two halves are easiest to confuse:
// a sealed key is opaque bytes, so a sweep written against the plaintext column
// alone would report success and leave the credential in place, and one written
// too widely would destroy a live key that opens perfectly.
func TestASealedStrandedStreamKeyIsClearedAndASealedRTMPOneSurvives(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polyemesis.db")
	box := testBox(t)

	first := keyDB(t, path, WithSecretBox(box))
	stranded := validDest()
	stranded.Name = "Recorder"
	stranded.StreamKey = strandedSentinel
	strandedSaved, err := first.CreateDestination(stranded)
	if err != nil {
		t.Fatalf("create the one to strand: %v", err)
	}
	live := validDest()
	live.Name = "Live"
	live.StreamKey = strandedSentinel
	liveSaved, err := first.CreateDestination(live)
	if err != nil {
		t.Fatalf("create the live one: %v", err)
	}
	// Both keys are in stream_key_enc, because a box is configured.
	var enc []byte
	if err := first.sql.QueryRow(`SELECT stream_key_enc FROM destinations WHERE id=?`,
		strandedSaved.ID).Scan(&enc); err != nil {
		t.Fatalf("read the sealed column: %v", err)
	}
	if len(enc) == 0 {
		t.Fatalf("the key was not sealed, so this test is not measuring the sealed path")
	}
	strandRow(t, first, strandedSaved.ID, DestFile, "recording.mkv")
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second := keyDB(t, path, WithSecretBox(box))
	backStranded, err := second.GetDestination(strandedSaved.ID)
	if err != nil {
		t.Fatalf("read back the stranded one: %v", err)
	}
	if backStranded.StreamKey != "" {
		t.Errorf("the sealed stranded key survived Open")
	}
	if backStranded.KeyUnreadable != "" {
		t.Errorf("clearing the sealed column left the destination looking unreadable "+
			"rather than keyless: %q", backStranded.KeyUnreadable)
	}
	var strandedEnc []byte
	if err := second.sql.QueryRow(`SELECT stream_key_enc FROM destinations WHERE id=?`,
		strandedSaved.ID).Scan(&strandedEnc); err != nil {
		t.Fatalf("read the sealed column back: %v", err)
	}
	if len(strandedEnc) != 0 {
		t.Errorf("the sealed stream_key_enc column still holds the stranded key")
	}

	backLive, err := second.GetDestination(liveSaved.ID)
	if err != nil {
		t.Fatalf("read back the live one: %v", err)
	}
	if backLive.StreamKey != strandedSentinel {
		t.Errorf("the sealed key of an RTMP destination did not open after the sweep")
	}
}

// TestARenameStillKeepsAnUnreadableKeySealed is the control for the write path
// this change sits next to. keepsSealedPrimaryKey exists because a save that
// stored the mask pointed a destination at a URL that never existed, and a
// stranded-key sweep that reached into the same columns is exactly the kind of
// change that could quietly undo it.
func TestARenameStillKeepsAnUnreadableKeySealed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polyemesis.db")

	first := keyDB(t, path, WithSecretBox(testBox(t)))
	dst := validDest()
	dst.StreamKey = strandedSentinel
	saved, err := first.CreateDestination(dst)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// The wrong key file: the row reads back keyless and flagged.
	second := keyDB(t, path, WithSecretBox(otherBox(t)))
	back, err := second.GetDestination(saved.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if back.KeyUnreadable == "" {
		t.Fatalf("the destination did not report an unreadable key, so this test is not " +
			"measuring the guard it is named for")
	}
	back.Name = "Renamed on the wrong machine"
	if _, err := second.UpdateDestination(back); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// The right key file back: the ciphertext must still be there.
	third := keyDB(t, path, WithSecretBox(testBox(t)))
	recovered, err := third.GetDestination(saved.ID)
	if err != nil {
		t.Fatalf("read back after recovery: %v", err)
	}
	if recovered.StreamKey != strandedSentinel {
		t.Errorf("the sealed key did not come back after the right key file was restored")
	}
	if recovered.Name != "Renamed on the wrong machine" {
		t.Errorf("the rename was lost: %q", recovered.Name)
	}
}
