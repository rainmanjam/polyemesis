// Package publish sends a finished clip or short to a platform (Wave 4 #5c).
//
// THREE RULES SHAPE EVERYTHING HERE.
//
//  1. Nothing publishes without a human click. This package has no scheduler,
//     no timer and no entry point of its own: the only thing that starts a
//     publish is a job on the queue, and the worker refuses any job whose
//     ledger row was not armed by the session-only publish handler (see
//     db.BeginPublication). There is no auto-publish setting because there is
//     nothing for one to switch.
//  2. Never post twice. One ledger row per clip + platform + account holds the
//     platform's post id, and every irreversible step is preceded by durable
//     proof of intent, so a retry resumes or asks the platform rather than
//     starting again.
//  3. Credentials stay where the OAuth flow put them. The worker is handed a
//     short-lived access token by the same refresh path the rest of the
//     product uses, and nothing here stores, logs or returns one.
package publish

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"unicode/utf16"

	"github.com/rainmanjam/polyemesis/internal/jobs"
)

// JobKind is what a publish is registered as on the queue.
const JobKind jobs.Kind = "clip.publish"

// Platform ids. These are the values the API and the ledger carry.
const (
	PlatformYouTube = "youtube"
	PlatformTikTok  = "tiktok"
	PlatformX       = "x"
)

// Privacy levels for a YouTube post. TikTok has exactly one (see tiktok.go).
const (
	PrivacyPrivate  = "private"
	PrivacyUnlisted = "unlisted"
	PrivacyPublic   = "public"
)

// XPublishEnv is the operator's switch for publishing to X. It is separate from
// POLYEMESIS_X_LIVESTREAM on purpose: that one says X has whitelisted the app
// for Livestream, and nothing about it says posting is wanted.
const XPublishEnv = "POLYEMESIS_X_PUBLISH"

// XGateCopy is the sentence shown while X publishing is off.
const XGateCopy = "X publishing is switched off. It stays off until an operator turns it on in the server's environment."

// Gates are the operator switches. The zero value has everything gated off.
type Gates struct {
	XPublish bool
}

// GatesFromEnv reads the switches. Anything other than 1 or true leaves X off.
func GatesFromEnv() Gates {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(XPublishEnv))) {
	case "1", "true":
		return Gates{XPublish: true}
	}
	return Gates{}
}

// Meta is what the human reviews and confirms.
type Meta struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	Privacy     string   `json:"privacy"`
}

// Limits, in the platform's own units.
const (
	maxYouTubeTitle       = 100
	maxYouTubeDescription = 5000
	maxYouTubeTagsChars   = 450 // YouTube allows 500; leave room for the quoting it adds
	maxTikTokCaption      = 2200
	maxXPost              = 280
	maxTags               = 30
)

// ErrInvalid is wrapped by every validation failure.
var ErrInvalid = errors.New("invalid publish request")

// Normalize cleans the human's edits and checks them against the platform.
// It never invents content: it trims, strips leading '#' from tags, drops
// duplicates and empties, and rejects what the platform would refuse.
func Normalize(platform string, m Meta) (Meta, error) {
	m.Title = strings.TrimSpace(m.Title)
	m.Description = strings.TrimSpace(m.Description)
	seen := map[string]bool{}
	tags := make([]string, 0, len(m.Tags))
	for _, t := range m.Tags {
		t = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(t), "#"))
		t = strings.Join(strings.Fields(t), "")
		if t == "" || seen[strings.ToLower(t)] {
			continue
		}
		seen[strings.ToLower(t)] = true
		tags = append(tags, t)
	}
	m.Tags = tags
	if len(m.Tags) > maxTags {
		return m, fmt.Errorf("%w: at most %d hashtags", ErrInvalid, maxTags)
	}

	switch platform {
	case PlatformYouTube:
		if m.Title == "" {
			return m, fmt.Errorf("%w: a title is required", ErrInvalid)
		}
		if n := utf16Len(m.Title); n > maxYouTubeTitle {
			return m, fmt.Errorf("%w: the title is %d characters; YouTube allows %d", ErrInvalid, n, maxYouTubeTitle)
		}
		if strings.ContainsAny(m.Title, "<>") || strings.ContainsAny(m.Description, "<>") {
			return m, fmt.Errorf("%w: YouTube does not accept < or > in a title or description", ErrInvalid)
		}
		if len(m.Description) > maxYouTubeDescription {
			return m, fmt.Errorf("%w: the description is longer than YouTube's %d bytes", ErrInvalid, maxYouTubeDescription)
		}
		total := 0
		for _, t := range m.Tags {
			total += len(t) + 1
		}
		if total > maxYouTubeTagsChars {
			return m, fmt.Errorf("%w: the hashtags are too long for YouTube", ErrInvalid)
		}
		switch m.Privacy {
		case "":
			m.Privacy = PrivacyPrivate
		case PrivacyPrivate, PrivacyUnlisted, PrivacyPublic:
		default:
			return m, fmt.Errorf("%w: unknown privacy %q", ErrInvalid, m.Privacy)
		}
	case PlatformTikTok:
		// There is no choice to make: until TikTok audits the app every post
		// must be private, so whatever the request carried is replaced.
		m.Privacy = TikTokPrivacy
		if utf16Len(captionFor(m)) > maxTikTokCaption {
			return m, fmt.Errorf("%w: the caption and hashtags are longer than TikTok's %d characters", ErrInvalid, maxTikTokCaption)
		}
	case PlatformX:
		m.Privacy = PrivacyPublic
		if utf16Len(xTextFor(m)) > maxXPost {
			return m, fmt.Errorf("%w: the post text is longer than X's %d characters", ErrInvalid, maxXPost)
		}
	default:
		return m, fmt.Errorf("%w: unknown platform %q", ErrInvalid, platform)
	}
	return m, nil
}

func utf16Len(s string) int { return len(utf16.Encode([]rune(s))) }

// hashtags renders tags as "#a #b".
func hashtags(tags []string) string {
	parts := make([]string, 0, len(tags))
	for _, t := range tags {
		parts = append(parts, "#"+t)
	}
	return strings.Join(parts, " ")
}

// captionFor is the TikTok caption: title, description, hashtags.
func captionFor(m Meta) string {
	var parts []string
	for _, p := range []string{m.Title, m.Description, hashtags(m.Tags)} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, "\n")
}

// xTextFor is the X post text: title, then hashtags.
func xTextFor(m Meta) string {
	var parts []string
	for _, p := range []string{m.Title, m.Description, hashtags(m.Tags)} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, "\n")
}

// ---------------------------------------------------------------- failures

// Cause says why a publish did not complete. The strings are carried to the UI
// and to webhooks; add new ones, never rename.
type Cause string

const (
	// CauseNeedsReconnect: the connected account's grant lacks the permission
	// to post. One click on Reconnect fixes it.
	CauseNeedsReconnect Cause = "needs_reconnect"
	// CauseTokenRejected: the platform refused the access token.
	CauseTokenRejected Cause = "token_rejected"
	// CauseQuota: the platform's daily allowance is spent.
	CauseQuota Cause = "quota_exceeded"
	// CauseRateLimited: slow down; retried with backoff.
	CauseRateLimited Cause = "rate_limited"
	// CauseUnavailable: the platform answered 5xx or timed out; retried.
	CauseUnavailable Cause = "platform_unavailable"
	// CauseNetwork: DNS, refused or reset; retried.
	CauseNetwork Cause = "network"
	// CauseClipMissing: the exported file is gone from disk.
	CauseClipMissing Cause = "clip_missing"
	// CauseClipRejected: the platform will not take this video (length, format).
	CauseClipRejected Cause = "clip_rejected"
	// CausePrivacyUnavailable: TikTok offered no private option for this account.
	CausePrivacyUnavailable Cause = "privacy_unavailable"
	// CauseRefused: the platform said no and gave a reason.
	CauseRefused Cause = "platform_refused"
	// CauseOutcomeUnknown: the last step may or may not have posted; nothing
	// was repeated. Check the account before publishing again.
	CauseOutcomeUnknown Cause = "outcome_unknown"
	// CauseGated: this platform is switched off.
	CauseGated Cause = "gated"
	// CauseNotConfirmed: the job was not started by a confirmed click.
	CauseNotConfirmed Cause = "not_confirmed"
	// CauseCancelled: a human cancelled the job. Cancelling revokes the click;
	// publishing again needs a new one.
	CauseCancelled Cause = "cancelled"
	// CauseUnknown carries the raw line.
	CauseUnknown Cause = "unknown"
)

// Reason is the failure shape shown on a publication; it matches
// failreason.Reason field for field where they overlap (cause, line).
type Reason struct {
	Cause Cause  `json:"cause"`
	Line  string `json:"line,omitempty"`
}

// Failure is an adapter's verdict on a failed step.
type Failure struct {
	Cause  Cause
	Detail string
	// Retry is true when waiting and trying again can help.
	Retry bool
}

func (f *Failure) Error() string { return string(f.Cause) + ": " + f.Detail }

func fail(c Cause, retry bool, format string, a ...any) *Failure {
	return &Failure{Cause: c, Retry: retry, Detail: fmt.Sprintf(format, a...)}
}

// GrantFailure reports whether a stored grant is enough to post to a platform,
// without constructing an adapter. nil means it is.
func GrantFailure(platform, scopes string) *Failure {
	c := Credential{Scopes: scopes}
	switch platform {
	case PlatformYouTube:
		return (&YouTube{}).Ready(c)
	case PlatformTikTok:
		return (&TikTok{}).Ready(c)
	case PlatformX:
		return (&X{}).Ready(c)
	}
	return fail(CauseUnknown, false, "unknown platform %q", platform)
}

// TokenGate hands the worker a TokenSource that is filled in later. The queue
// and its workers start before the API server exists, and the token refresh
// path lives on the server; until Set is called the source answers with a
// retryable "starting up".
type TokenGate struct {
	mu sync.RWMutex
	fn TokenSource
}

// Source is the TokenSource to give the worker.
func (g *TokenGate) Source() TokenSource {
	return func(ctx context.Context, accountID int64) (Credential, error) {
		g.mu.RLock()
		fn := g.fn
		g.mu.RUnlock()
		if fn == nil {
			return Credential{}, errors.New("the server is still starting; this will be retried")
		}
		return fn(ctx, accountID)
	}
}

// Set installs the real source.
func (g *TokenGate) Set(fn TokenSource) {
	g.mu.Lock()
	g.fn = fn
	g.mu.Unlock()
}
