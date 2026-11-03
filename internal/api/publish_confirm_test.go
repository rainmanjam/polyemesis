package api

import (
	"testing"
	"time"
)

func TestConfirmTokenIsSingleUse(t *testing.T) {
	b := newConfirmBook(time.Now)
	tok, _ := b.issue(1, "hash-a")
	if err := b.redeem(tok, 1, "hash-a"); err != nil {
		t.Fatalf("first redeem: %v", err)
	}
	if err := b.redeem(tok, 1, "hash-a"); err == nil {
		t.Fatal("a token was redeemed twice")
	}
}

func TestConfirmTokenBindsContentAndPerson(t *testing.T) {
	b := newConfirmBook(time.Now)
	tok, _ := b.issue(1, "hash-a")
	if err := b.redeem(tok, 1, "hash-b"); err == nil {
		t.Fatal("a token confirmed different content than it was issued for")
	}
	// A mismatch burns the token: it cannot be probed with guesses.
	if err := b.redeem(tok, 1, "hash-a"); err == nil {
		t.Fatal("a token survived a mismatched redeem")
	}
	tok, _ = b.issue(1, "hash-a")
	if err := b.redeem(tok, 2, "hash-a"); err == nil {
		t.Fatal("a token was redeemed by a different person")
	}
}

func TestConfirmTokenExpires(t *testing.T) {
	now := time.Now()
	b := newConfirmBook(func() time.Time { return now })
	tok, exp := b.issue(1, "h")
	if !exp.After(now) {
		t.Fatalf("expiry %v is not in the future", exp)
	}
	now = now.Add(confirmTTL + time.Second)
	if err := b.redeem(tok, 1, "h"); err == nil {
		t.Fatal("an expired token was accepted")
	}
}

func TestConfirmBookRefusesEmptyAndUnknownTokens(t *testing.T) {
	b := newConfirmBook(time.Now)
	for _, tok := range []string{"", "nope"} {
		if err := b.redeem(tok, 1, "h"); err == nil {
			t.Errorf("token %q was accepted", tok)
		}
	}
}

func TestConfirmBookIsBounded(t *testing.T) {
	b := newConfirmBook(time.Now)
	first, _ := b.issue(1, "h")
	for i := 0; i < confirmMax+5; i++ {
		b.issue(1, "h")
	}
	if len(b.m) > confirmMax {
		t.Fatalf("the book holds %d tokens, cap is %d", len(b.m), confirmMax)
	}
	if err := b.redeem(first, 1, "h"); err == nil {
		t.Error("the oldest token survived eviction")
	}
}
