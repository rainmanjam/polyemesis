package db

import (
	"strings"
	"testing"
)

// The X chat access mode and broadcast language live in the compliance JSON
// (Compliance.X), so they need no column. These tests prove they round-trip,
// that nothing set means nothing stored, and that a bad value is refused at save.

func xChat(opt int, loc string) *XBroadcastControls {
	return &XBroadcastControls{ChatOption: &opt, Locale: loc}
}

// MUTATION: the X field's json tag renamed. Observed: FAIL, the read back was nil.
func TestAnXDestinationKeepsItsChatModeAndLanguageThroughCreateAndUpdate(t *testing.T) {
	d := testDB(t)
	in := validDest()
	in.Platform = PlatformX
	in.Compliance.X = xChat(4, "pt-BR")
	created, err := d.CreateDestination(in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := d.GetDestination(created.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if x := got.Compliance.X; x == nil || x.ChatOption == nil || *x.ChatOption != 4 || x.Locale != "pt-BR" {
		t.Fatalf("after create = %+v, want chat option 4 and pt-BR", x)
	}
	got.Compliance.X = xChat(0, "")
	if _, err := d.UpdateDestination(got); err != nil {
		t.Fatalf("update: %v", err)
	}
	again, err := d.GetDestination(created.ID)
	if err != nil {
		t.Fatalf("read back after update: %v", err)
	}
	// 0 is a real choice ("default"), distinct from unset.
	if x := again.Compliance.X; x == nil || x.ChatOption == nil || *x.ChatOption != 0 {
		t.Fatalf("after update = %+v, want an explicit chat option 0", x)
	}
}

// MUTATION: omitempty dropped from ChatOption. Observed: FAIL, an unset value stored "chatOption":null.
func TestAnUnsetXChatModeStoresNothingAndDoesNotCountAsCompliance(t *testing.T) {
	d := testDB(t)
	in := validDest()
	in.Platform = PlatformX
	created, err := d.CreateDestination(in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var raw string
	if err := d.sql.QueryRow(`SELECT compliance FROM destinations WHERE id=?`, created.ID).Scan(&raw); err != nil {
		t.Fatalf("read raw: %v", err)
	}
	if strings.Contains(raw, "chatOption") || strings.Contains(raw, `"x"`) {
		t.Fatalf("stored compliance %q mentions X for a destination that set nothing", raw)
	}
	c := Compliance{X: xChat(2, "en")}
	if !c.Empty() {
		t.Fatalf("X chat controls made Compliance non-empty: they are not pushed by a CompliancePusher")
	}
}

func TestAnInvalidXChatModeOrLanguageIsRefusedAtSave(t *testing.T) {
	for name, x := range map[string]*XBroadcastControls{
		"chat option 7":  xChat(7, ""),
		"chat option -1": xChat(-1, ""),
		"a sentence":     xChat(2, "english please"),
		"digits":         xChat(2, "12"),
	} {
		in := validDest()
		in.Platform = PlatformX
		in.Compliance.X = x
		if err := in.Validate(); err == nil || !strings.Contains(err.Error(), "X ") {
			t.Errorf("%s: Validate = %v, want an X refusal", name, err)
		}
	}
	for _, x := range []*XBroadcastControls{nil, {}, xChat(0, ""), xChat(6, "en"), xChat(3, "zh-Hans"), xChat(2, "en_US")} {
		in := validDest()
		in.Platform = PlatformX
		in.Compliance.X = x
		if err := in.Validate(); err != nil {
			t.Errorf("Validate(%+v) = %v, want accepted", x, err)
		}
	}
}

func TestXChatModeIsRefusedWhereNothingWouldSendIt(t *testing.T) {
	in := validDest()
	in.Platform = PlatformTwitch
	in.Compliance.X = xChat(2, "")
	if err := in.Validate(); err == nil || !strings.Contains(err.Error(), "only to X") {
		t.Fatalf("Validate on Twitch = %v, want an only-to-X refusal", err)
	}
}

// The setting is in the destination's own row, so deleting the destination
// deletes it: nothing is left behind to sweep.
func TestXChatModeGoesWhenTheDestinationIsDeleted(t *testing.T) {
	d := testDB(t)
	in := validDest()
	in.Platform = PlatformX
	in.Compliance.X = xChat(5, "ja")
	created, err := d.CreateDestination(in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := d.DeleteDestination(created.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	var n int
	if err := d.sql.QueryRow(`SELECT COUNT(*) FROM destinations WHERE compliance LIKE '%chatOption%'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rows still carrying a chat option after delete: %d, %v", n, err)
	}
}
