package db

import (
	"fmt"

	"github.com/rainmanjam/polyemesis/internal/platform"
)

// MigrateWave3XTitle adds destinations.go_live_title, the title the lifecycle
// coordinator sends when it publishes an X broadcast (#3). X takes a title only
// on the PUBLISH transition, so the title is stored before the show rather than
// pushed during it. The default is the empty string, which sends no title and
// leaves X's own, so an upgrade changes nothing. Idempotent, like every slot.
func (d *DB) MigrateWave3XTitle() error {
	has, err := columnExists(d.sql, "destinations", "go_live_title")
	if err != nil {
		return fmt.Errorf("inspect destinations columns: %w", err)
	}
	if has {
		return nil
	}
	if _, err := d.sql.Exec(`ALTER TABLE destinations ADD COLUMN go_live_title TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("add destinations.go_live_title: %w", err)
	}
	return nil
}

// XGoLiveTitleMax bounds the X go-live title. X documents the PUBLISH title as
// "subject to X post length limits" without a number, so the 280-character
// post limit is used. PROVISIONAL until a live PUBLISH on a whitelisted app
// confirms or corrects it.
const XGoLiveTitleMax = platform.XGoLiveTitleMax

// MigrateWave3XTweet adds destinations.x_go_live_tweet, when an X destination
// wants X's announcement post as its broadcast is published (see
// Destination.XGoLiveTweet). The default is the empty string, read as "first",
// the first go-live of a show only. Idempotent, like every slot.
func (d *DB) MigrateWave3XTweet() error {
	has, err := columnExists(d.sql, "destinations", "x_go_live_tweet")
	if err != nil {
		return fmt.Errorf("inspect destinations columns: %w", err)
	}
	if has {
		return nil
	}
	if _, err := d.sql.Exec(`ALTER TABLE destinations ADD COLUMN x_go_live_tweet TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("add destinations.x_go_live_tweet: %w", err)
	}
	return nil
}

// The three X go-live announcement choices (Destination.XGoLiveTweet).
const (
	// XTweetFirstGoLive posts on the first go-live of a show only. The empty
	// value means the same, so an upgraded row gets it.
	XTweetFirstGoLive = "first"
	// XTweetEveryGoLive posts on every go-live, renewals included: what X does
	// when it is not told otherwise.
	XTweetEveryGoLive = "every"
	// XTweetNever never posts.
	XTweetNever = "never"
)

func validXGoLiveTweet(v string) bool {
	switch v {
	case "", XTweetFirstGoLive, XTweetEveryGoLive, XTweetNever:
		return true
	}
	return false
}

// XGoLiveTweetMode is the destination's announcement choice with the empty
// value read as XTweetFirstGoLive.
func (d *Destination) XGoLiveTweetMode() string {
	if d == nil || d.XGoLiveTweet == "" {
		return XTweetFirstGoLive
	}
	return d.XGoLiveTweet
}
