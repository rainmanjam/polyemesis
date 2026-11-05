package db

import (
	"errors"
	"path/filepath"
	"testing"
)

func partialWriteDestination(t *testing.T, d *DB) *Destination {
	t.Helper()
	created, err := d.CreateDestination(&Destination{
		Name: "fb", Kind: DestRTMP, Platform: PlatformFacebook,
		URL: "rtmps://live.example/rtmp", StreamKey: "original-key",
		AudioBitrate: 128, Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	return created
}

// The operator's edits that land while a handler is away at the platform: a
// stop, a rename and a routing change. Made through the store's own writers,
// as the stop route and the editor's save make them.
func landOperatorEdits(t *testing.T, d *DB, id int64) {
	t.Helper()
	if err := d.SetDestinationEnabled(id, false); err != nil {
		t.Fatalf("stop: %v", err)
	}
	row, err := d.GetDestination(id)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}
	row.Name = "renamed by the operator"
	row.Profile.Tracks[0].Gain = 0.5
	if _, err := d.UpdateDestination(row); err != nil {
		t.Fatalf("rename and routing change: %v", err)
	}
}

func assertOperatorEditsSurvived(t *testing.T, got *Destination) {
	t.Helper()
	if got.Enabled {
		t.Error("the destination stopped during the write is enabled again, so the reconcile " +
			"that follows starts it")
	}
	if got.Name != "renamed by the operator" {
		t.Errorf("Name = %q: the rename made during the write was reverted", got.Name)
	}
	if g := got.Profile.Tracks[0].Gain; g != 0.5 {
		t.Errorf("track 1 gain = %v: the routing change made during the write was reverted", g)
	}
}

// Refresh stream key holds a destination across a platform call that can take
// thirty seconds. Its write owns the ingest columns, and nothing else: a stop,
// a rename and a routing change made in that window must survive it, even when
// the callback is handed a snapshot from before them and writes it back.
//
// Mutation: in UpdateDestinationIngest's write, also store name, enabled and
// profile from cur (`UPDATE destinations SET name=?, enabled=?, profile=?` in
// the same transaction). Observed red: all three operator edits reverted.
func TestTheIngestWriteLeavesEveryOtherColumnAlone(t *testing.T) {
	d := testDB(t)
	created := partialWriteDestination(t, d)
	stale := *created // what the handler read before it went to the platform

	landOperatorEdits(t, d, created.ID)

	got, err := d.UpdateDestinationIngest(created.ID, nil, func(cur *Destination) error {
		cur.URL = "rtmps://new-ingest.example/rtmp"
		cur.StreamKey = "key-from-the-platform"
		cur.BackupURL, cur.BackupStreamKey = "rtmps://backup.example/rtmp", "backup-key"
		// The handler's stale copy, written back over the row. All dropped.
		cur.Name, cur.Enabled, cur.Profile = stale.Name, stale.Enabled, stale.Profile
		cur.AudioBitrate = 320
		cur.ExtraOutputArgs = "-metadata title=stale"
		return nil
	})
	if err != nil {
		t.Fatalf("UpdateDestinationIngest: %v", err)
	}
	if got.URL != "rtmps://new-ingest.example/rtmp" || got.StreamKey != "key-from-the-platform" {
		t.Errorf("ingest = %q / %q, want what the platform answered", got.URL, got.StreamKey)
	}
	if got.BackupURL != "rtmps://backup.example/rtmp" || got.BackupStreamKey != "backup-key" {
		t.Errorf("backup = %q / %q, want it stored", got.BackupURL, got.BackupStreamKey)
	}
	assertOperatorEditsSurvived(t, got)
	if got.AudioBitrate != 128 || got.ExtraOutputArgs != "" {
		t.Errorf("AudioBitrate %d / expert %q: the ingest write reached columns it does not own",
			got.AudioBitrate, got.ExtraOutputArgs)
	}

	// The answer is the stored row, not the callback's copy of it.
	stored, err := d.GetDestination(created.ID)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}
	if stored.Name != got.Name || stored.Enabled != got.Enabled || stored.StreamKey != got.StreamKey {
		t.Errorf("returned %+v, stored %+v", got, stored)
	}
}

// The expert save's write owns the three expert columns, and nothing else.
//
// Mutation: the same extra UPDATE of name, enabled and profile in
// UpdateDestinationExpertArgs's write. Observed red: all three operator edits
// reverted.
func TestTheExpertArgsWriteLeavesEveryOtherColumnAlone(t *testing.T) {
	d := testDB(t)
	created := partialWriteDestination(t, d)
	stale := *created

	landOperatorEdits(t, d, created.ID)

	got, err := d.UpdateDestinationExpertArgs(created.ID, func(cur *Destination) error {
		cur.ExtraInputArgs = "-thread_queue_size 1024"
		cur.ExtraOutputArgs = "-metadata title=x"
		cur.ExpertAckReencode = true
		cur.Name, cur.Enabled, cur.Profile = stale.Name, stale.Enabled, stale.Profile
		cur.StreamKey, cur.URL = "stale-key", "rtmps://stale.example/rtmp"
		return nil
	})
	if err != nil {
		t.Fatalf("UpdateDestinationExpertArgs: %v", err)
	}
	if got.ExtraInputArgs != "-thread_queue_size 1024" || got.ExtraOutputArgs != "-metadata title=x" ||
		!got.ExpertAckReencode {
		t.Errorf("expert = %q / %q / %v, want all three stored",
			got.ExtraInputArgs, got.ExtraOutputArgs, got.ExpertAckReencode)
	}
	assertOperatorEditsSurvived(t, got)
	if got.StreamKey != "original-key" || got.URL != "rtmps://live.example/rtmp" {
		t.Errorf("ingest %q / %q: the expert write reached columns it does not own", got.URL, got.StreamKey)
	}
}

// The callback is shown the row as it stands now, which is what lets the API
// run its repair and its account check against the truth rather than the
// snapshot it took before the platform call.
//
// Mutation: in writeDestinationPart, replace the scan with
// `cur, err := &Destination{ID: id}, error(nil)`, which is the callback being
// shown something other than the stored row. Observed red: the callback saw an
// empty name.
func TestThePartialWritesApplyToTheRowAsItStandsNow(t *testing.T) {
	d := testDB(t)
	for name, write := range map[string]func(int64, func(*Destination) error) (*Destination, error){
		"ingest": func(id int64, apply func(*Destination) error) (*Destination, error) {
			return d.UpdateDestinationIngest(id, nil, apply)
		},
		"expert": d.UpdateDestinationExpertArgs,
	} {
		t.Run(name, func(t *testing.T) {
			created := partialWriteDestination(t, d)
			landOperatorEdits(t, d, created.ID)
			var saw string
			_, err := write(created.ID, func(cur *Destination) error {
				saw = cur.Name
				return nil
			})
			// What the callback saw first: a stale row may well fail to
			// validate too, and that error would hide the reason.
			if saw != "renamed by the operator" {
				t.Fatalf("the callback was shown %q, not the row as it stands", saw)
			}
			if err != nil {
				t.Fatalf("write: %v", err)
			}
		})
	}
}

// A callback that refuses writes nothing, and its error is what comes back.
//
// Mutation: in writeDestinationPart, ignore apply's error. Observed red: the
// refusal came back as success (err = nil), and the refused key was written.
func TestARefusedPartialWriteStoresNothing(t *testing.T) {
	d := testDB(t)
	created := partialWriteDestination(t, d)
	refusal := errors.New("not this row")
	for _, want := range []error{ErrIngestSkipped, refusal} {
		_, err := d.UpdateDestinationIngest(created.ID, nil, func(cur *Destination) error {
			cur.StreamKey = "should-not-land"
			return want
		})
		if !errors.Is(err, want) {
			t.Fatalf("err = %v, want %v", err, want)
		}
	}
	got, _ := d.GetDestination(created.ID)
	if got.StreamKey != "original-key" {
		t.Errorf("StreamKey = %q after a refused write, want it untouched", got.StreamKey)
	}
}

// Both writers check the row as UpdateDestination does, so neither can store
// what that one refuses.
//
// Mutation: drop the Validate call in writeDestinationPart. Observed red: an
// RTMP destination with no URL was stored.
func TestThePartialWritesValidateTheRowTheyStore(t *testing.T) {
	d := testDB(t)
	created := partialWriteDestination(t, d)
	if _, err := d.UpdateDestinationIngest(created.ID, nil, func(cur *Destination) error {
		cur.URL = "" // an RTMP destination with nowhere to send to
		return nil
	}); err == nil {
		t.Fatal("an RTMP destination with no URL was stored")
	}
	got, _ := d.GetDestination(created.ID)
	if got.URL != "rtmps://live.example/rtmp" {
		t.Errorf("URL = %q after a refused write", got.URL)
	}
	if _, err := d.UpdateDestinationIngest(404, nil, func(*Destination) error { return nil }); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing row: err = %v, want ErrNotFound", err)
	}
}

// A key refresh on a destination whose stored keys cannot be opened replaces
// the primary and must not destroy the backup's ciphertext: the same per-half
// rule UpdateDestination keeps (TestRetypingThePrimaryKeyLeavesTheBackupCiphertextAlone).
//
// Mutation: in UpdateDestinationIngest, ignore the per-half decision and reseal
// both halves with destIngestQuery. Observed red: the key refresh destroyed the
// backup key's ciphertext.
func TestTheIngestWriteKeepsAnUnreadableBackupKeysCiphertext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polyemesis.db")
	d := keyDB(t, path, WithSecretBox(testBox(t)))
	src := &Source{Name: "Main", Enabled: true, Ingest: DefaultSettings().Ingest}
	if err := d.CreateSource(src); err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	saved, err := d.CreateDestination(&Destination{
		Name: "has both halves", Kind: DestRTMP, URL: "rtmp://example.invalid/live",
		StreamKey: "primary-secret", BackupStreamKey: "backup-secret", AudioBitrate: 160,
	})
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	sealedBefore := rawCol(t, d, "backup_stream_key_enc", saved.ID)

	if _, err := d.UpdateDestinationIngest(saved.ID, nil, func(cur *Destination) error {
		// The row as it reads back after a restore with no key file.
		cur.KeyUnreadable = "the stored key could not be opened with this secret.key"
		cur.StreamKey = "key-from-the-platform"
		cur.BackupStreamKey = ""
		return nil
	}); err != nil {
		t.Fatalf("UpdateDestinationIngest: %v", err)
	}
	if got := rawCol(t, d, "backup_stream_key_enc", saved.ID); string(got) != string(sealedBefore) {
		t.Error("the key refresh destroyed or rewrote the backup key's ciphertext")
	}
	if got, err := d.GetDestination(saved.ID); err != nil || got.StreamKey != "key-from-the-platform" {
		t.Errorf("StreamKey = %v (%v), want the fetched key sealed and stored", got, err)
	}
}

// A destination whose captions were switched off while a key refresh was
// fetching has no use for the caption URL that refresh stored, and the write
// forgets it, as UpdateDestination does.
//
// Mutation: skip the caption delete in writeDestinationPart. Observed red: the
// caption URL was still stored.
func TestTheIngestWriteForgetsACaptionURLTheRowNoLongerUses(t *testing.T) {
	// The caption URL is stored sealed, so this one needs a key.
	d := keyDB(t, filepath.Join(t.TempDir(), "polyemesis.db"), WithSecretBox(testBox(t)))
	if err := d.CreateSource(&Source{Name: "Main", Enabled: true, Ingest: DefaultSettings().Ingest}); err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	created := partialWriteDestination(t, d) // Facebook: posts no captions to YouTube
	if err := d.PutCaptionIngestURL(created.ID, "key-from-the-platform", "https://captions.example/x"); err != nil {
		t.Fatalf("PutCaptionIngestURL: %v", err)
	}
	if _, err := d.UpdateDestinationIngest(created.ID, nil, func(cur *Destination) error {
		cur.StreamKey = "key-from-the-platform"
		return nil
	}); err != nil {
		t.Fatalf("UpdateDestinationIngest: %v", err)
	}
	if has, err := d.HasCaptionIngestURL(created.ID, "key-from-the-platform"); err != nil || has {
		t.Errorf("caption URL still stored (%v, %v) for a destination that posts no captions", has, err)
	}
}

// A key refresh's caption ingestion URL is stored in the same transaction as
// the key it belongs to: the row handed back reports it ready, a refused write
// stores neither, and a row that no longer posts captions is not given one.
//
// Mutation: delete the caption upsert from UpdateDestinationIngest. Observed
// red: the returned row is not ready, and no URL is stored for the new key.
// The refused case is the transaction's own guarantee: the handler used to
// store the URL before its write, and that ordering is what it pins.
func TestTheIngestWriteStoresTheCaptionURLWithItsKey(t *testing.T) {
	d := keyDB(t, filepath.Join(t.TempDir(), "polyemesis.db"), WithSecretBox(testBox(t)))
	if err := d.CreateSource(&Source{Name: "Main", Enabled: true, Ingest: DefaultSettings().Ingest}); err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	newCaptioned := func(name string) *Destination {
		t.Helper()
		created, err := d.CreateDestination(&Destination{
			Name: name, Kind: DestRTMP, Platform: PlatformYouTube,
			URL: "rtmp://a.example/live2", StreamKey: "held-key", AudioBitrate: 128,
			DelaySeconds: 30, CaptionMode: "youtube_post",
		})
		if err != nil {
			t.Fatalf("CreateDestination: %v", err)
		}
		return created
	}
	const captionURL = "https://upload.youtube.com/closedcaption?cid=fake"

	t.Run("stored with the key", func(t *testing.T) {
		dst := newCaptioned("stored")
		got, err := d.UpdateDestinationIngest(dst.ID, &IngestCaption{URL: captionURL}, func(cur *Destination) error {
			cur.StreamKey = "fresh-key"
			return nil
		})
		if err != nil {
			t.Fatalf("UpdateDestinationIngest: %v", err)
		}
		if !got.CaptionIngestReady {
			t.Error("the row handed back says captions are not ready, though the URL came with the key")
		}
		if u, _ := d.CaptionIngestURL(dst.ID, "fresh-key"); u != captionURL {
			t.Errorf("CaptionIngestURL(fresh-key) = %q, want the URL stored for the new key", u)
		}
	})

	t.Run("a refused write stores neither", func(t *testing.T) {
		dst := newCaptioned("refused")
		if _, err := d.UpdateDestinationIngest(dst.ID, &IngestCaption{URL: captionURL}, func(cur *Destination) error {
			cur.StreamKey = "fresh-key"
			return ErrIngestSkipped
		}); !errors.Is(err, ErrIngestSkipped) {
			t.Fatalf("err = %v, want ErrIngestSkipped", err)
		}
		if has, _ := d.HasCaptionIngestURL(dst.ID, "fresh-key"); has {
			t.Error("a caption URL was stored for a key refresh that was refused")
		}
	})

	t.Run("an install with no secret key keeps the key and says why", func(t *testing.T) {
		plain := testDB(t) // no secret box: a caption URL cannot be stored sealed
		dst, err := plain.CreateDestination(&Destination{
			Name: "no box", Kind: DestRTMP, Platform: PlatformYouTube,
			URL: "rtmp://a.example/live2", StreamKey: "held-key", AudioBitrate: 128,
			DelaySeconds: 30, CaptionMode: "youtube_post",
		})
		if err != nil {
			t.Fatalf("CreateDestination: %v", err)
		}
		caption := &IngestCaption{URL: captionURL}
		got, err := plain.UpdateDestinationIngest(dst.ID, caption, func(cur *Destination) error {
			cur.StreamKey = "fresh-key"
			return nil
		})
		if err != nil {
			t.Fatalf("the key was refused because its caption URL could not be sealed: %v", err)
		}
		if got.StreamKey != "fresh-key" {
			t.Errorf("StreamKey = %q, want the fetched key stored", got.StreamKey)
		}
		if !errors.Is(caption.NotStored, ErrCaptionIngestUnsealable) {
			t.Errorf("NotStored = %v, want ErrCaptionIngestUnsealable so the handler can say why", caption.NotStored)
		}
	})

	t.Run("captions switched off during the fetch", func(t *testing.T) {
		dst := newCaptioned("switched off")
		row, _ := d.GetDestination(dst.ID)
		row.CaptionMode = "off"
		if _, err := d.UpdateDestination(row); err != nil {
			t.Fatalf("switch captions off: %v", err)
		}
		if _, err := d.UpdateDestinationIngest(dst.ID, &IngestCaption{URL: captionURL}, func(cur *Destination) error {
			cur.StreamKey = "fresh-key"
			return nil
		}); err != nil {
			t.Fatalf("UpdateDestinationIngest: %v", err)
		}
		if has, _ := d.HasCaptionIngestURL(dst.ID, "fresh-key"); has {
			t.Error("a caption URL was stored for a destination that no longer posts captions")
		}
	})
}
