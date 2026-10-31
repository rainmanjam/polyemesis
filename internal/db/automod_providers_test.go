package db

import (
	"encoding/json"
	"testing"
)

// A model block written before #21 has no protocol, because there was only
// one: OpenAI-compatible. Decoded over today's defaults it must NOT inherit the
// new Jev default -- that would send Jev-shaped bodies to the operator's
// OpenAI endpoint, or the operator's OpenAI key to TypeSafe's. This blob is
// the shape the previous version stored, endpoint, key flag and all.
func TestAPreProviderModelBlockIsReadAsOpenAICompatibleNotAsTheNewJevDefault(t *testing.T) {
	legacy := []byte(`{"enabled":true,"on":{},"history":{},
		"model":{"enabled":true,"endpoint":"https://api.openai.com/v1/chat/completions",
		"model":"gpt-4o-mini","hasApiKey":true,"timeoutSeconds":4,"maxCallsPerHour":500,
		"action":"timeout","minConfidence":0.8,"instruction":"x"}}`)
	a := mergeBaseSettings().Automod
	if err := json.Unmarshal(legacy, &a); err != nil {
		t.Fatal(err)
	}
	if a.Model.Protocol != AutomodProtocolOpenAI || a.Model.Preset != "custom" {
		t.Fatalf("legacy block read as protocol=%q preset=%q; want openai/custom", a.Model.Protocol, a.Model.Preset)
	}
	if a.Model.Endpoint != "https://api.openai.com/v1/chat/completions" || a.Model.Action != "timeout" {
		t.Fatalf("legacy values lost: %+v", a.Model)
	}

	// The blank, never-configured shape too: still OpenAI, so a key stored
	// against it is never handed to the Jev default endpoint.
	blank := []byte(`{"model":{"enabled":false,"endpoint":"","model":"gpt-4o-mini"}}`)
	a = mergeBaseSettings().Automod
	json.Unmarshal(blank, &a)
	if a.Model.Protocol != AutomodProtocolOpenAI {
		t.Fatalf("a blank legacy block became %q", a.Model.Protocol)
	}

	// And a block that DOES name its protocol is taken at its word, so an
	// operator who chose Jev is never re-read as OpenAI.
	chosen := []byte(`{"model":{"protocol":"jev","preset":"jev","endpoint":"https://api.typesafe.ai/v1/systemone"}}`)
	a = mergeBaseSettings().Automod
	json.Unmarshal(chosen, &a)
	if a.Model.Protocol != AutomodProtocolJev || a.Model.Preset != "jev" {
		t.Fatalf("an explicit jev block became %q/%q", a.Model.Protocol, a.Model.Preset)
	}
	// A document with no model block at all changes nothing: fresh installs
	// keep the Jev default.
	a = mergeBaseSettings().Automod
	json.Unmarshal([]byte(`{"enabled":true}`), &a)
	if a.Model.Protocol != AutomodProtocolJev || a.Secondary.Protocol != AutomodProtocolOpenAI {
		t.Fatalf("defaults: primary %q secondary %q", a.Model.Protocol, a.Secondary.Protocol)
	}
}

// The category table REPLACES on decode, for the same reason On does: a
// category the client dropped to mean "back to the default" must not keep its
// stored action. An absent table changes nothing.
func TestTheCategoryActionTableIsReplacedNotMerged(t *testing.T) {
	a := mergeBaseSettings().Automod
	a.Model.CategoryActions = map[string]string{"spam": "delete", "threat": "ban"}
	json.Unmarshal([]byte(`{"model":{"protocol":"jev","categoryActions":{"spam":"delete"}}}`), &a)
	if _, kept := a.Model.CategoryActions["threat"]; kept || a.Model.CategoryActions["spam"] != "delete" {
		t.Fatalf("table merged: %v", a.Model.CategoryActions)
	}
	json.Unmarshal([]byte(`{"model":{"protocol":"jev","instruction":"y"}}`), &a)
	if a.Model.CategoryActions["spam"] != "delete" {
		t.Fatalf("an absent table cleared the stored one: %v", a.Model.CategoryActions)
	}
}

// An unknown protocol is refused, for either checker, whether or not it is
// enabled -- it is the protocol the checker wakes up with.
func TestAnUnknownAutomodProtocolIsRefused(t *testing.T) {
	for _, set := range []func(*Settings){
		func(s *Settings) { s.Automod.Model.Protocol = "grpc" },
		func(s *Settings) { s.Automod.Secondary.Protocol = "grpc" },
		func(s *Settings) { s.Automod.Secondary.Enabled = true; s.Automod.Secondary.MinConfidence = 80 },
	} {
		s := mergeBaseSettings()
		set(&s)
		if s.Validate() == nil {
			t.Errorf("accepted %+v / %+v", s.Automod.Model, s.Automod.Secondary)
		}
	}
	if err := mergeBaseSettings().Validate(); err != nil {
		t.Fatalf("defaults refused: %v", err)
	}
}

// Each checker's key lives in its own sealed row, and setting or clearing
// either forgets THAT checker's Test pass and only that one.
func TestSecondaryKeyIsSealedSeparatelyAndAKeyChangeForgetsItsTestPass(t *testing.T) {
	d, box := testDB(t), testBox(t)
	// Built at runtime, so no credential-shaped literal sits in the source.
	pk, sk := "primary-"+"fake", "secondary-"+"fake"
	if err := d.PutAutomodKeyFor(box, "primary", pk); err != nil {
		t.Fatal(err)
	}
	if err := d.PutAutomodKeyFor(box, AutomodSlotSecondary, sk); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.GetAutomodKeyFor(box, "primary"); got != pk {
		t.Fatalf("primary = %q", got)
	}
	if got, _ := d.GetAutomodKeyFor(box, AutomodSlotSecondary); got != sk {
		t.Fatalf("secondary = %q", got)
	}
	var raw []byte
	d.sql.QueryRow(`SELECT key_enc FROM automod_secondary_creds WHERE id = 1`).Scan(&raw)
	if len(raw) == 0 || string(raw) == sk {
		t.Fatal("the secondary key is not sealed at rest")
	}

	d.PutAutomodTestPass("primary", "fp-p")
	d.PutAutomodTestPass(AutomodSlotSecondary, "fp-s")
	if err := d.PutAutomodKeyFor(box, AutomodSlotSecondary, ""); err != nil {
		t.Fatal(err)
	}
	if has, _ := d.HasAutomodKeyFor(AutomodSlotSecondary); has {
		t.Fatal("clearing the secondary key left it set")
	}
	if fp, _ := d.AutomodTestPass(AutomodSlotSecondary); fp != "" {
		t.Fatalf("the secondary's pass survived a key change: %q", fp)
	}
	if fp, _ := d.AutomodTestPass("primary"); fp != "fp-p" {
		t.Fatalf("the primary's pass was touched by the secondary's key: %q", fp)
	}
	d.PutAutomodKeyFor(box, "primary", "rotated-"+"fake")
	if fp, _ := d.AutomodTestPass("primary"); fp != "" {
		t.Fatal("the primary's pass survived a key rotation")
	}
}
