package db

import (
	"bytes"
	"errors"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/secrets"
)

func pubBox(t *testing.T) *secrets.Box {
	t.Helper()
	b, err := secrets.New(bytes.Repeat([]byte{0x11}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func sampleClaim() Publication {
	return Publication{
		ClipJobID: 7, Platform: "youtube", AccountID: 3, AccountName: "Channel",
		Title: "A title", Description: "words", Tags: []string{"a", "b"},
		Privacy: "private", ContentHash: "h1", Nonce: "n1", ConfirmedBy: 1,
	}
}

func TestPublicationTableIsAdditiveAndIdempotent(t *testing.T) {
	d := testDB(t)
	before := SchemaVersion()
	for i := 0; i < 2; i++ {
		if err := d.MigrateClipPublications(); err != nil {
			t.Fatalf("migration %d: %v", i, err)
		}
	}
	if SchemaVersion() != before {
		t.Errorf("an additive table moved the schema version")
	}
}

// The unique key is the double-post guard: a second claim of the same
// clip+platform+account must NOT create a second row.
func TestClaimIsOnePerClipPlatformAccount(t *testing.T) {
	d := testDB(t)
	first, out, err := d.ClaimPublication(sampleClaim())
	if err != nil || out != ClaimCreated {
		t.Fatalf("first claim = %v, %v", out, err)
	}
	if first.IdemKey != PublicationKey(7, "youtube", 3) {
		t.Errorf("idempotency key = %q", first.IdemKey)
	}
	again := sampleClaim()
	again.Nonce = "n2"
	second, out, err := d.ClaimPublication(again)
	if err != nil || out != ClaimInFlight {
		t.Fatalf("second claim = %v, %v; want ClaimInFlight", out, err)
	}
	if second.ID != first.ID || second.Nonce != "n1" {
		t.Errorf("second claim replaced the first: %+v", second)
	}
	all, _ := d.ListPublicationsForClip(7)
	if len(all) != 1 {
		t.Errorf("rows = %d, want 1", len(all))
	}
	// A different account is a different post.
	other := sampleClaim()
	other.AccountID = 4
	if _, out, _ := d.ClaimPublication(other); out != ClaimCreated {
		t.Errorf("a second account = %v, want ClaimCreated", out)
	}
}

// Begin is the worker's gate: only the nonce minted at the click may advance it.
func TestBeginRequiresTheNonceFromTheClick(t *testing.T) {
	d := testDB(t)
	p, _, _ := d.ClaimPublication(sampleClaim())
	if _, err := d.BeginPublication(p.ID, "forged"); !errors.Is(err, ErrPublicationNotArmed) {
		t.Fatalf("a wrong nonce = %v, want ErrPublicationNotArmed", err)
	}
	if _, err := d.BeginPublication(p.ID, ""); !errors.Is(err, ErrPublicationNotArmed) {
		t.Fatalf("an empty nonce = %v, want ErrPublicationNotArmed", err)
	}
	got, err := d.BeginPublication(p.ID, "n1")
	if err != nil || got.State != PublicationUploading || got.Attempts != 1 {
		t.Fatalf("begin = %+v, %v", got, err)
	}
	// An automatic retry is the same click continuing.
	if got, err = d.BeginPublication(p.ID, "n1"); err != nil || got.Attempts != 2 {
		t.Fatalf("second begin = %+v, %v", got, err)
	}
}

func TestAFailedRowCannotBeBegunUntilRearmedByAClick(t *testing.T) {
	d := testDB(t)
	p, _, _ := d.ClaimPublication(sampleClaim())
	if err := d.FailPublication(p.ID, "quota", "out of quota"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.BeginPublication(p.ID, "n1"); !errors.Is(err, ErrPublicationNotArmed) {
		t.Fatalf("begin on a failed row = %v, want ErrPublicationNotArmed", err)
	}
	re := sampleClaim()
	re.Nonce = "n2"
	re.Title = "Edited"
	row, out, err := d.ClaimPublication(re)
	if err != nil || out != ClaimRearmed || row.Title != "Edited" || row.FailCause != "" {
		t.Fatalf("rearm = %+v, %v, %v", row, out, err)
	}
	if _, err := d.BeginPublication(p.ID, "n1"); !errors.Is(err, ErrPublicationNotArmed) {
		t.Fatalf("the OLD job's nonce must stay dead after a rearm, got %v", err)
	}
	if _, err := d.BeginPublication(p.ID, "n2"); err != nil {
		t.Fatalf("the new nonce = %v", err)
	}
}

func TestAPostedRowIsNeverRearmedOrBegun(t *testing.T) {
	d := testDB(t)
	p, _, _ := d.ClaimPublication(sampleClaim())
	if _, err := d.BeginPublication(p.ID, "n1"); err != nil {
		t.Fatal(err)
	}
	if err := d.RecordPublicationPost(p.ID, "vid123", "https://youtu.be/vid123", "private"); err != nil {
		t.Fatal(err)
	}
	row, out, err := d.ClaimPublication(sampleClaim())
	if err != nil || out != ClaimPosted || row.PlatformPostID != "vid123" {
		t.Fatalf("claim over a posted row = %+v, %v, %v", row, out, err)
	}
	if _, err := d.BeginPublication(p.ID, "n1"); !errors.Is(err, ErrPublicationPosted) {
		t.Fatalf("begin on a posted row = %v, want ErrPublicationPosted", err)
	}
	// Recording the same post twice is harmless; a DIFFERENT id is refused.
	if err := d.RecordPublicationPost(p.ID, "vid123", "u", "private"); err != nil {
		t.Errorf("idempotent record = %v", err)
	}
	if err := d.RecordPublicationPost(p.ID, "other", "u", "private"); err == nil {
		t.Errorf("a second, different post id was accepted")
	}
}

func TestSessionSecretIsSealedAtRest(t *testing.T) {
	d := testDB(t)
	box := pubBox(t)
	p, _, _ := d.ClaimPublication(sampleClaim())
	const secret = "https://upload.example/session?token=abc"
	if err := d.SetPublicationSession(box, p.ID, secret, "meta1"); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := d.sql.QueryRow(`SELECT session_enc FROM clip_publications WHERE id = ?`, p.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("upload.example")) {
		t.Errorf("the session URI is stored in the clear")
	}
	got, hash, err := d.GetPublicationSession(box, p.ID)
	if err != nil || got != secret || hash != "meta1" {
		t.Errorf("session = %q %q %v", got, hash, err)
	}
	row, _ := d.GetPublication(p.ID)
	if !row.HasSession {
		t.Errorf("HasSession = false")
	}
	if err := d.ClearPublicationSession(p.ID); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := d.GetPublicationSession(box, p.ID); got != "" {
		t.Errorf("session survived Clear: %q", got)
	}
}

func TestStampIrreversibleOnce(t *testing.T) {
	d := testDB(t)
	p, _, _ := d.ClaimPublication(sampleClaim())
	first, err := d.StampPublicationFinalizing(p.ID)
	if err != nil || !first {
		t.Fatalf("first stamp = %v, %v", first, err)
	}
	if again, _ := d.StampPublicationFinalizing(p.ID); again {
		t.Errorf("the finalize stamp was granted twice")
	}
}

func TestJobNoteAndRemoteOnlyTouchAnInFlightRowWithTheLiveNonce(t *testing.T) {
	d := testDB(t)
	p, _, _ := d.ClaimPublication(sampleClaim())
	if err := d.SetPublicationJob(p.ID, "wrong-nonce", 99); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.GetPublication(p.ID); got.PublishJobID != 0 {
		t.Fatalf("a job was attached with the wrong nonce: %+v", got)
	}
	if err := d.SetPublicationJob(p.ID, "n1", 42); err != nil {
		t.Fatal(err)
	}
	if err := d.SetPublicationNote(p.ID, "will retry: busy"); err != nil {
		t.Fatal(err)
	}
	if err := d.SetPublicationRemote(p.ID, "v_pub_file~9"); err != nil {
		t.Fatal(err)
	}
	got, _ := d.GetPublication(p.ID)
	if got.PublishJobID != 42 || got.FailDetail != "will retry: busy" || got.PlatformPostID != "v_pub_file~9" {
		t.Fatalf("row = %+v", got)
	}
	if err := d.FailPublication(p.ID, "network", "gone"); err != nil {
		t.Fatal(err)
	}
	// A failed row takes no more notes or remote ids: the note is the failure.
	_ = d.SetPublicationNote(p.ID, "ignored")
	_ = d.SetPublicationRemote(p.ID, "ignored")
	got, _ = d.GetPublication(p.ID)
	if got.FailDetail != "gone" || got.PlatformPostID != "v_pub_file~9" {
		t.Fatalf("a failed row was edited: %+v", got)
	}
}

func TestAMissingPublicationIsNotFoundAndANilTagListIsEmpty(t *testing.T) {
	d := testDB(t)
	if _, err := d.GetPublication(404); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	c := sampleClaim()
	c.Tags = nil
	p, _, err := d.ClaimPublication(c)
	if err != nil || p.Tags == nil || len(p.Tags) != 0 {
		t.Fatalf("tags = %#v, %v", p.Tags, err)
	}
	for _, bad := range []Publication{{}, {ClipJobID: 1, Platform: "youtube", AccountID: 1}} {
		if _, _, err := d.ClaimPublication(bad); err == nil {
			t.Errorf("claim %+v was accepted", bad)
		}
	}
	if err := d.RecordPublicationPost(p.ID, "", "", ""); err == nil {
		t.Error("a post without an id was recorded")
	}
	if err := d.RecordPublicationPost(9999, "x", "", ""); err == nil {
		t.Error("a post was recorded on a missing row")
	}
}
