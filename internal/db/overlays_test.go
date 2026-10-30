package db

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestAnOverlayKeyRotatesAndRevokes(t *testing.T) {
	d := testDB(t)
	o, err := d.CreateChatOverlay("  Twitch scene ", []string{"twitch"}, json.RawMessage(`{"preset":"classic"}`))
	if err != nil {
		t.Fatal(err)
	}
	if o.Name != "Twitch scene" || !strings.HasPrefix(o.OBSKey, OverlayKeyPrefix) || len(o.OBSKey) < 40 {
		t.Fatalf("created %+v", o)
	}
	if got, err := d.ChatOverlayByKey(o.OBSKey); err != nil || got.ID != o.ID || got.Platforms[0] != "twitch" {
		t.Fatalf("by key: %+v %v", got, err)
	}

	rot, err := d.RotateChatOverlayKey(o.ID)
	if err != nil || rot.OBSKey == o.OBSKey {
		t.Fatalf("rotate: %+v %v", rot, err)
	}
	if _, err := d.ChatOverlayByKey(o.OBSKey); !errors.Is(err, ErrOverlayNotFound) {
		t.Errorf("the rotated-away key still resolves: %v", err)
	}

	rev, err := d.RevokeChatOverlayKey(o.ID)
	if err != nil || rev.OBSKey != "" {
		t.Fatalf("revoke: %+v %v", rev, err)
	}
	for _, k := range []string{rot.OBSKey, "", OverlayKeyPrefix, "pmk_x"} {
		if _, err := d.ChatOverlayByKey(k); !errors.Is(err, ErrOverlayNotFound) {
			t.Errorf("key %q resolves after revoke: %v", k, err)
		}
	}
	// Restyling keeps the key: the URL in OBS must survive a colour change.
	again, _ := d.RotateChatOverlayKey(o.ID)
	up, err := d.UpdateChatOverlay(o.ID, "All", nil, json.RawMessage(`{"preset":"bubble"}`))
	if err != nil || up.OBSKey != again.OBSKey || len(up.Platforms) != 0 || string(up.Style) != `{"preset":"bubble"}` {
		t.Fatalf("update: %+v %v", up, err)
	}
	if err := d.DeleteChatOverlay(o.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ChatOverlayByKey(again.OBSKey); !errors.Is(err, ErrOverlayNotFound) {
		t.Errorf("a deleted overlay's key still resolves")
	}
	if _, err := d.RotateChatOverlayKey(o.ID); !errors.Is(err, ErrOverlayNotFound) {
		t.Errorf("rotate on a deleted overlay: %v", err)
	}
}

func TestAnOverlayNeedsAName(t *testing.T) {
	d := testDB(t)
	if _, err := d.CreateChatOverlay(" ", nil, nil); err == nil {
		t.Error("an unnamed overlay was created")
	}
	if _, err := d.CreateChatOverlay(strings.Repeat("x", 65), nil, nil); err == nil {
		t.Error("a 65-character name was accepted")
	}
	list, err := d.ListChatOverlays()
	if err != nil || len(list) != 0 {
		t.Fatalf("list %v %v", list, err)
	}
}
