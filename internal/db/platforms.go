package db

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/rainmanjam/polyemesis/internal/secrets"
)

// PlatformCreds is the operator's own OAuth developer app. polyemesis cannot
// ship client secrets, so the user registers an app and pastes the pair in.
type PlatformCreds struct {
	Platform     Platform  `json:"platform"`
	ClientID     string    `json:"clientId"`
	ClientSecret string    `json:"-"` // never serialised outward
	HasSecret    bool      `json:"hasSecret"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// PutPlatformCreds stores a client ID/secret pair, encrypting the secret.
func (d *DB) PutPlatformCreds(box *secrets.Box, p Platform, clientID, clientSecret string) error {
	enc, err := box.Seal(clientSecret)
	if err != nil {
		return err
	}
	_, err = d.sql.Exec(`INSERT INTO platform_creds (platform, client_id, client_secret_enc, updated_at)
		VALUES (?,?,?,?)
		ON CONFLICT(platform) DO UPDATE SET client_id=excluded.client_id,
			client_secret_enc=excluded.client_secret_enc, updated_at=excluded.updated_at`,
		p, clientID, enc, time.Now().Unix())
	return err
}

// GetPlatformCreds loads and decrypts a credential pair.
func (d *DB) GetPlatformCreds(box *secrets.Box, p Platform) (*PlatformCreds, error) {
	var (
		c       PlatformCreds
		enc     []byte
		updated int64
	)
	err := d.sql.QueryRow(`SELECT platform, client_id, client_secret_enc, updated_at
		FROM platform_creds WHERE platform = ?`, p).Scan(&c.Platform, &c.ClientID, &enc, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if c.ClientSecret, err = box.Open(enc); err != nil {
		return nil, err
	}
	c.HasSecret = c.ClientSecret != ""
	c.UpdatedAt = time.Unix(updated, 0)
	return &c, nil
}

// ListPlatformCreds returns credentials without secrets, for the settings UI.
func (d *DB) ListPlatformCreds() ([]PlatformCreds, error) {
	rows, err := d.sql.Query(`SELECT platform, client_id, LENGTH(client_secret_enc), updated_at FROM platform_creds`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []PlatformCreds{}
	for rows.Next() {
		var (
			c       PlatformCreds
			n       int
			updated int64
		)
		if err := rows.Scan(&c.Platform, &c.ClientID, &n, &updated); err != nil {
			return nil, err
		}
		c.HasSecret = n > 0
		c.UpdatedAt = time.Unix(updated, 0)
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeletePlatformCreds removes a developer app registration.
func (d *DB) DeletePlatformCreds(p Platform) error {
	_, err := d.sql.Exec(`DELETE FROM platform_creds WHERE platform = ?`, p)
	return err
}

// PlatformAccount is a connected channel. Multiple per platform is supported
// and is the point: two YouTube channels are two accounts.
type PlatformAccount struct {
	ID           int64     `json:"id"`
	Platform     Platform  `json:"platform"`
	AccountName  string    `json:"accountName"`
	AccountRef   string    `json:"accountRef"`
	AccessToken  string    `json:"-"`
	RefreshToken string    `json:"-"`
	ExpiresAt    time.Time `json:"expiresAt"`
	Scopes       string    `json:"scopes"`
	// ScopeVer is the provider's ScopeVersion at the moment this account was
	// connected. Compared against the provider's CURRENT version to spot a
	// token that predates a scope change -- see oauth.Provider.ScopeVersion.
	//
	// Zero means "connected before this column existed", which is not the same
	// as "out of date": the API layer falls back to comparing the granted
	// scopes for those rows rather than accusing every existing account.
	ScopeVer  int       `json:"scopeVer"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// String redacts AccessToken and RefreshToken so a stray %v/%+v -- in a log
// line, a wrapped error, a test failure message -- cannot print a live
// token. AccessToken/RefreshToken already carry `json:"-"` for the API
// boundary; this closes the same hole for Go's own formatting and logging,
// which json:"-" does nothing against. Defined on the value receiver: Go
// always includes value-receiver methods in the pointer's method set (never
// the other way round), so this single definition is also what fmt reaches
// for a *PlatformAccount. #16.
func (a PlatformAccount) String() string {
	return fmt.Sprintf("PlatformAccount{ID:%d, Platform:%s, AccountName:%q, AccountRef:%q, "+
		"AccessToken:%s, RefreshToken:%s, ExpiresAt:%s, Scopes:%q, ScopeVer:%d}",
		a.ID, a.Platform, a.AccountName, a.AccountRef,
		redactSecret(a.AccessToken), redactSecret(a.RefreshToken), a.ExpiresAt, a.Scopes, a.ScopeVer)
}

// LogValue redacts the same fields for slog, so slog.Any("account", acct) is
// as safe as fmt-printing it.
func (a PlatformAccount) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Int64("id", a.ID),
		slog.String("platform", string(a.Platform)),
		slog.String("account_name", a.AccountName),
		slog.String("account_ref", a.AccountRef),
		slog.String("access_token", redactSecret(a.AccessToken)),
		slog.String("refresh_token", redactSecret(a.RefreshToken)),
		slog.Time("expires_at", a.ExpiresAt),
		slog.String("scopes", a.Scopes),
		slog.Int("scope_ver", a.ScopeVer),
	)
}

// redactSecret stands in for a secret value in a String()/LogValue(); it
// leaves an unset secret visibly empty rather than claiming one is present.
func redactSecret(s string) string {
	if s == "" {
		return ""
	}
	return "[redacted]"
}

// Expired reports whether the access token needs refreshing. The one-minute
// skew keeps us from handing a token to an API call that will outlive it.
func (a PlatformAccount) Expired() bool {
	if a.ExpiresAt.IsZero() || a.ExpiresAt.Unix() == 0 {
		return false
	}
	return time.Now().Add(time.Minute).After(a.ExpiresAt)
}

// UpsertPlatformAccount stores a connected account, replacing any previous
// tokens for the same (platform, accountRef).
func (d *DB) UpsertPlatformAccount(box *secrets.Box, a *PlatformAccount) (*PlatformAccount, error) {
	accessEnc, err := box.Seal(a.AccessToken)
	if err != nil {
		return nil, err
	}
	refreshEnc, err := box.Seal(a.RefreshToken)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	var exp int64
	if !a.ExpiresAt.IsZero() {
		exp = a.ExpiresAt.Unix()
	}

	_, err = d.sql.Exec(`INSERT INTO platform_accounts
		(platform, account_name, account_ref, access_token_enc, refresh_token_enc, expires_at, scopes, scope_ver, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(platform, account_ref) DO UPDATE SET
			account_name=excluded.account_name,
			access_token_enc=excluded.access_token_enc,
			refresh_token_enc=COALESCE(NULLIF(excluded.refresh_token_enc, X''), platform_accounts.refresh_token_enc),
			expires_at=excluded.expires_at,
			scopes=excluded.scopes,
			scope_ver=excluded.scope_ver,
			updated_at=excluded.updated_at`,
		a.Platform, a.AccountName, a.AccountRef, accessEnc, refreshEnc, exp, a.Scopes, a.ScopeVer, now, now)
	if err != nil {
		return nil, err
	}

	var id int64
	if err := d.sql.QueryRow(`SELECT id FROM platform_accounts WHERE platform=? AND account_ref=?`,
		a.Platform, a.AccountRef).Scan(&id); err != nil {
		return nil, err
	}
	return d.GetPlatformAccount(box, id)
}

// GetPlatformAccount loads and decrypts one account.
func (d *DB) GetPlatformAccount(box *secrets.Box, id int64) (*PlatformAccount, error) {
	var (
		a                 PlatformAccount
		accessEnc         []byte
		refreshEnc        []byte
		exp, created, upd int64
	)
	err := d.sql.QueryRow(`SELECT id, platform, account_name, account_ref, access_token_enc,
		refresh_token_enc, expires_at, scopes, scope_ver, created_at, updated_at
		FROM platform_accounts WHERE id = ?`, id).
		Scan(&a.ID, &a.Platform, &a.AccountName, &a.AccountRef, &accessEnc, &refreshEnc,
			&exp, &a.Scopes, &a.ScopeVer, &created, &upd)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if a.AccessToken, err = box.Open(accessEnc); err != nil {
		return nil, err
	}
	if a.RefreshToken, err = box.Open(refreshEnc); err != nil {
		return nil, err
	}
	if exp > 0 {
		a.ExpiresAt = time.Unix(exp, 0)
	}
	a.CreatedAt = time.Unix(created, 0)
	a.UpdatedAt = time.Unix(upd, 0)
	return &a, nil
}

// ListPlatformAccounts returns all connected accounts, without token material.
func (d *DB) ListPlatformAccounts() ([]PlatformAccount, error) {
	rows, err := d.sql.Query(`SELECT id, platform, account_name, account_ref, expires_at,
		scopes, scope_ver, created_at, updated_at FROM platform_accounts ORDER BY platform, account_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []PlatformAccount{}
	for rows.Next() {
		var (
			a                 PlatformAccount
			exp, created, upd int64
		)
		if err := rows.Scan(&a.ID, &a.Platform, &a.AccountName, &a.AccountRef, &exp,
			&a.Scopes, &a.ScopeVer, &created, &upd); err != nil {
			return nil, err
		}
		if exp > 0 {
			a.ExpiresAt = time.Unix(exp, 0)
		}
		a.CreatedAt = time.Unix(created, 0)
		a.UpdatedAt = time.Unix(upd, 0)
		out = append(out, a)
	}
	return out, rows.Err()
}

// DeletePlatformAccount disconnects an account.
func (d *DB) DeletePlatformAccount(id int64) error {
	res, err := d.sql.Exec(`DELETE FROM platform_accounts WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- OAuth state (CSRF protection for the authorization-code flow) ---

// PutOAuthState records a pending authorization request.
func (d *DB) PutOAuthState(state string, p Platform, verifier string) error {
	// Opportunistically drop states older than 10 minutes; an authorization
	// round trip that takes longer than that has been abandoned.
	_, _ = d.sql.Exec(`DELETE FROM oauth_states WHERE created_at < ?`, time.Now().Add(-10*time.Minute).Unix())
	_, err := d.sql.Exec(`INSERT INTO oauth_states (state, platform, verifier, created_at) VALUES (?,?,?,?)`,
		state, p, verifier, time.Now().Unix())
	return err
}

// TakeOAuthState consumes a state parameter, returning its platform and PKCE
// verifier. Single-use: a replayed callback finds nothing.
//
// SELECT-then-DELETE would not actually be single-use: db.go's
// SetMaxOpenConns(1) holds the pool's one connection only for the duration of
// each statement, not across the two, so two concurrent callbacks with the
// same state could both pass the SELECT before either's DELETE ran. DELETE
// ... RETURNING (SQLite 3.35+, confirmed against modernc.org/sqlite in
// TestTakeOAuthStateReturningWorksWithThisDriver) makes the read and the
// consume one statement, so a second caller finds the row already gone. #8.
func (d *DB) TakeOAuthState(state string) (Platform, string, error) {
	var (
		p        Platform
		verifier string
		created  int64
	)
	err := d.sql.QueryRow(`DELETE FROM oauth_states WHERE state = ? RETURNING platform, verifier, created_at`, state).
		Scan(&p, &verifier, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", errors.New("unknown or already-used OAuth state")
	}
	if err != nil {
		return "", "", err
	}
	if time.Since(time.Unix(created, 0)) > 10*time.Minute {
		return "", "", errors.New("OAuth state expired; please retry the connection")
	}
	return p, verifier, nil
}

// --- Destination preset catalogue -------------------------------------------
//
// A preset is documentation with a form attached: it fills in the transport and
// the ingest URL for a platform so the operator types a stream key and nothing
// else. It is deliberately not a Platform — Platform is the small set of
// integrations that have code behind them (OAuth key fetch), and it must stay
// small because destinations.Validate() rejects unknown values. Presets are
// additive: all but the four that map to a Platform save as PlatformCustom, so
// the catalogue can grow to a hundred entries without touching validation, the
// engine, or a single stored row.
//
// A preset that guesses is worse than a preset that admits ignorance. Where the
// ingest hostname is issued per account, per event or per region — which is most
// platforms — URL is empty and Notes says where to copy it from. An empty field
// costs the operator one visit to a dashboard they already have open; a wrong
// hostname costs them a broadcast, and it fails as a connection timeout that
// looks like their network.

// VideoGuidance is what a PLATFORM publishes about encoder settings — never
// what polyemesis thinks it should be.
//
// The distinction is the whole reason this type carries a Source and a Checked
// date, and why both are required rather than optional. This catalogue already
// ships ingest hostnames, which move without notice, under a disclaimer saying
// exactly that. Numbers move faster. A bitrate figure with no provenance is the
// "confidently wrong" failure the disclaimer exists to prevent, and it would be
// worse than shipping nothing: an operator has no way to tell a researched
// number from a guess once it is in a form field.
//
// ADVISORY, ALWAYS. This seeds a form and annotates a picker. It must never
// hide an option, refuse a value, or block a save. datarhei's `allowCopy`
// filter — which suppresses the passthrough option when the source codec is not
// in a platform's accepted list — was considered for this codebase and
// rejected: suggesting is honest, hiding on the strength of a third-party
// number is not.
//
// A preset with no VideoGuidance ships none. "Not published" is a real answer
// and the catalogue says it rather than interpolating from a neighbour.
type VideoGuidance struct {
	// Width/Height/FPS describe the standard broadcast the platform documents.
	// Zero means the platform does not state one.
	Width  int `json:"width,omitempty"`
	Height int `json:"height,omitempty"`
	FPS    int `json:"fps,omitempty"`

	// KbpsMin/KbpsMax bound the platform's recommended VIDEO bitrate. Equal
	// values mean it publishes a single figure rather than a range.
	KbpsMin int `json:"kbpsMin,omitempty"`
	KbpsMax int `json:"kbpsMax,omitempty"`

	// GOPSeconds is the keyframe interval the platform asks for. Several
	// platforms refuse or degrade a stream that ignores this, which is why it
	// sits beside the bitrate rather than in a note.
	GOPSeconds float64 `json:"gopSeconds,omitempty"`

	// Note carries what a number cannot: tier gating, transcode availability,
	// and anything the platform qualifies its own figures with. Twitch is the
	// reason this is not optional in practice — its guidance differs by
	// partner/affiliate status and its transcodes are not guaranteed, and
	// flattening that into one bitrate would mislead exactly the operators who
	// most need it.
	Note string `json:"note,omitempty"`

	// Source is the URL this came from, and Checked is when it was last read.
	// Both required: a figure whose provenance cannot be shown is not shippable
	// here, and a figure nobody has re-read in a year should say so itself
	// rather than wait to be discovered wrong during a broadcast.
	Source  string `json:"source"`
	Checked string `json:"checked"`
}

// PlatformPresetDisclaimer is the wording the UI must show beside the
// catalogue. Ingest hostnames and platform limits move without notice, so no
// preset is presented as authoritative and every field stays editable.
const PlatformPresetDisclaimer = "Starting point — ingest URLs and limits change, so verify with the platform before you go live."

// PresetTransport is how a preset's ingest is reached. It is intentionally
// finer-grained than DestKind: rtmp and rtmps are one DestKind but two
// different things to tell an operator, and "hls" names a transport we can give
// guidance about without being able to publish to it.
type PresetTransport string

const (
	PresetRTMP  PresetTransport = "rtmp"
	PresetRTMPS PresetTransport = "rtmps"
	PresetSRT   PresetTransport = "srt"
	PresetHLS   PresetTransport = "hls"
)

// PresetGroup keeps a list of thirty navigable.
type PresetGroup string

const (
	GroupMajor      PresetGroup = "major"
	GroupVideo      PresetGroup = "video"
	GroupSelfHosted PresetGroup = "selfhosted"
	GroupCloud      PresetGroup = "cloud"
	GroupGeneric    PresetGroup = "generic"
)

// PresetGroupInfo is one heading in the picker.
type PresetGroupInfo struct {
	Key   PresetGroup `json:"key"`
	Label string      `json:"label"`
}

// PresetGroups returns the headings in display order.
func PresetGroups() []PresetGroupInfo {
	return []PresetGroupInfo{
		{GroupMajor, "Major"},
		{GroupVideo, "Video platforms"},
		{GroupSelfHosted, "Self-hosted"},
		{GroupCloud, "CDN & cloud"},
		{GroupGeneric, "Generic"},
	}
}

// DestinationPreset is one entry in the catalogue.
type DestinationPreset struct {
	ID    string      `json:"id"`
	Name  string      `json:"name"`
	Group PresetGroup `json:"group"`

	Transport PresetTransport `json:"transport"`

	// Kind is the destination transport this preset creates. Empty means
	// polyemesis cannot publish over this preset's transport yet, and the entry
	// exists only to say so rather than to leave the operator searching.
	Kind DestKind `json:"kind,omitempty"`

	// Platform links a preset to an integration with code behind it. Empty
	// means the destination saves as PlatformCustom, which is every preset that
	// is only a URL and a note.
	Platform Platform `json:"platform,omitempty"`

	// URL is the ingest template. A {placeholder} marks what the operator must
	// replace. Empty means we do not know this platform's hostname and refuse
	// to invent one — Notes says where to find it instead.
	URL string `json:"url"`

	// Video is the platform's own published encoder guidance, or nil when it
	// publishes none. Advisory: it seeds forms and annotates choices, and never
	// gates anything.
	Video *VideoGuidance `json:"video,omitempty"`

	// SeparateKey reports whether the platform issues a stream key distinct
	// from the URL. SRT is the usual false: its stream id rides in the query
	// string, so there is nothing to put in a second field.
	SeparateKey bool `json:"separateKey"`

	HelpURL string `json:"helpUrl,omitempty"`
	Notes   string `json:"notes"`

	// Aliases are extra search terms: the name an operator types is not always
	// the name on the tin ("twitter" for X, "meta" for Facebook).
	Aliases []string `json:"aliases,omitempty"`
}

// HasURL reports whether the preset can prefill an ingest URL at all.
func (p DestinationPreset) HasURL() bool { return p.URL != "" }

// Supported reports whether polyemesis can publish to this preset's transport.
func (p DestinationPreset) Supported() bool { return p.Kind != "" }

// destinationPresets is the catalogue. Order within a group is display order.
//
// Every URL here is either a hostname the platform documents publicly and has
// used for years, or a template whose {placeholders} make it obvious the
// operator must finish it. Anything else is empty on purpose; see the note at
// the top of this section.
var destinationPresets = []DestinationPreset{
	// ---------------------------------------------------------------- major
	{
		ID: "youtube", Name: "YouTube Live", Group: GroupMajor,
		Video: &VideoGuidance{
			Width: 1920, Height: 1080, FPS: 60, KbpsMin: 12000, KbpsMax: 12000, GOPSeconds: 2,
			Note:   "12000 is YouTube's RECOMMENDED H.264 figure for 1080p60, not a ceiling -- OBS's services.json carries 51000 as YouTube's maximum, a different fact rather than a contradiction. Keyframes must not exceed 4s -- beyond that YouTube reports gopSizeLong and the stream buffers. CBR.",
			Source: "https://support.google.com/youtube/answer/2853702", Checked: "2026-08-06",
		},
		Transport: PresetRTMP, Kind: DestRTMP, Platform: PlatformYouTube,
		URL:         "rtmp://a.rtmp.youtube.com/live2",
		SeparateKey: true,
		HelpURL:     "https://support.google.com/youtube/answer/2907883",
		Notes: "Connect a Google account in Settings → Platform credentials to fetch the stream key automatically, " +
			"or paste it from YouTube Studio → Go live → Stream. YouTube Studio also shows a backup ingest URL; " +
			"add it as a second destination if you want redundancy.",
		Aliases: []string{"google", "yt"},
	},
	{
		ID: "youtube-rtmps", Name: "YouTube Live (RTMPS)", Group: GroupMajor,
		Video: &VideoGuidance{
			Width: 1920, Height: 1080, FPS: 60, KbpsMin: 12000, KbpsMax: 12000, GOPSeconds: 2,
			Note:   "The same figures as YouTube's RTMP ingest, from the same page -- which recommends RTMPS over RTMP.",
			Source: "https://support.google.com/youtube/answer/2853702", Checked: "2026-08-06",
		},
		Transport: PresetRTMPS, Kind: DestRTMP, Platform: PlatformYouTube,
		URL:         "rtmps://a.rtmps.youtube.com/live2",
		SeparateKey: true,
		HelpURL:     "https://support.google.com/youtube/answer/2907883",
		Notes: "The same ingest over TLS on 443, for networks that block port 1935. The stream key is identical to " +
			"the plain-RTMP one.",
		Aliases: []string{"google", "yt", "tls"},
	},
	{
		ID: "twitch", Name: "Twitch", Group: GroupMajor,
		Video: &VideoGuidance{
			Width: 1920, Height: 1080, FPS: 60, KbpsMin: 6000, KbpsMax: 6000, GOPSeconds: 2,
			Note:   "Twitch's encoder guidance is the same for everyone; what is tiered is what happens AFTER ingest. Partners get transcodes on every broadcast, everyone else gets them on availability -- so a viewer on a slow connection may have no lower option. No maximum video bitrate is published. The two sources disagree on audio: Twitch's help page says 160 kbps maximum, OBS's services.json says 320.",
			Source: "https://help.twitch.tv/s/article/broadcasting-guidelines", Checked: "2026-08-06",
		},
		Transport: PresetRTMP, Kind: DestRTMP, Platform: PlatformTwitch,
		URL:         "rtmp://live.twitch.tv/app",
		SeparateKey: true,
		HelpURL:     "https://help.twitch.tv/s/article/broadcast-guidelines",
		Notes: "Connect a Twitch account in Settings → Platform credentials to fetch the stream key automatically. " +
			"live.twitch.tv resolves to a nearby ingest; if it picks a poor one, copy a specific regional ingest " +
			"host from Twitch's ingest list and paste it here instead.",
		Aliases: []string{"ttv"},
	},
	{
		ID: "twitch-rtmps", Name: "Twitch (RTMPS)", Group: GroupMajor,
		Transport: PresetRTMPS, Kind: DestRTMP, Platform: PlatformTwitch,
		URL:         "rtmps://live.twitch.tv/app",
		SeparateKey: true,
		HelpURL:     "https://help.twitch.tv/s/article/broadcast-guidelines",
		Notes: "RTMP over TLS on 443, for networks that block 1935. Twitch's TLS support is per-ingest: if this host " +
			"refuses the connection, use the plain Twitch preset or copy an RTMPS ingest from Twitch's ingest list.",
		Aliases: []string{"ttv", "tls"},
	},
	{
		ID: "kick", Name: "Kick", Group: GroupMajor,
		Video: &VideoGuidance{
			Width: 1920, Height: 1080, FPS: 60, KbpsMin: 1000, KbpsMax: 8000, GOPSeconds: 2,
			Note:   "H.264 only -- Kick refuses H.265 -- and CBR only; it does not accept VBR. 1920x1080 and 8000 kbps are stated as platform caps rather than advice. No audio bitrate is published: Kick's own FAQ asks the question twice and answers with sample rate and channels.",
			Source: "https://help.kick.com/en/articles/7066931-how-to-stream-on-kick-com", Checked: "2026-08-06",
		},
		Transport: PresetRTMPS, Kind: DestRTMP, Platform: PlatformKick,
		// EMPTY, and it must stay empty. Kick issues the ingest host per
		// channel, so anything hardcoded here is one particular person's --
		// and for a while this field held a real one, shipped in a public
		// repository, with a note asking the operator to replace it.
		//
		// The prefill was also wrong in a way the note did not cover: Kick's
		// dashboard prints the host with NO application path, and a preset
		// that copies that shape teaches it. rtmps://<host>/<key> makes the
		// stream key the RTMP app name, Amazon IVS refuses it, and the
		// destination reports "reconnecting" forever while producing nothing.
		// That cost a live debugging session; see internal/services.
		URL:         "",
		SeparateKey: true,
		HelpURL:     "https://kick.com/dashboard/settings/stream",
		// KEEP THIS IN STEP WITH oauth.PlatformCapabilities()'s kick row. It said
		// the key was manual for a long time, stayed saying it after the
		// capability matrix was corrected, and an operator reading this note was
		// told to copy by hand a key polyemesis can fetch for them.
		Notes: "Connect a Kick account in Settings → Platform credentials and polyemesis fetches the stream key " +
			"itself, over the streamkey:read scope — an account connected before that scope existed needs " +
			"reconnecting once. Without a connected account, copy the key from Kick → Settings → Stream. " +
			"Either way the ingest URL is yours to supply: Kick issues the host per channel, so there is nothing " +
			"to prefill here. APPEND /app to the URL Kick shows you — the dashboard prints it without one, and a " +
			"Kick destination without /app cannot publish at all. The result looks like " +
			"rtmps://<your-host>.global-contribute.live-video.net:443/app",
	},
	{
		ID: "facebook", Name: "Facebook Live", Group: GroupMajor,
		Video: &VideoGuidance{
			Width: 1920, Height: 1080, FPS: 60, KbpsMin: 4500, KbpsMax: 9000, GOPSeconds: 2,
			Note:   "H.264 video and AAC audio only; other formats may be rejected. Aspect ratio must be near 16:9 or the stream may not be supported. Keyframes must not exceed 4s. Eight hours maximum.",
			Source: "https://www.facebook.com/business/help/162540111070395", Checked: "2026-08-06",
		},
		Transport: PresetRTMPS, Kind: DestRTMP, Platform: PlatformFacebook,
		URL:         "rtmps://live-api-s.facebook.com:443/rtmp/",
		SeparateKey: true,
		HelpURL:     "https://www.facebook.com/live/producer",
		Notes: "Connect a Facebook account in Settings → Platform credentials and polyemesis creates the broadcast " +
			"and fills in both the ingest URL and the key. Note that Facebook issues them per broadcast, so each " +
			"refresh starts a new live video rather than re-reading an existing one. Registering the Meta app is " +
			"the slow part — it needs App Review before anyone but you can connect. To do it by hand instead, copy " +
			"the server URL and key from Live Producer. Facebook requires RTMPS; plain RTMP is refused.",
		Aliases: []string{"meta", "fb"},
	},
	{
		ID: "instagram", Name: "Instagram Live", Group: GroupMajor,
		Transport: PresetRTMPS, Kind: DestRTMP,
		SeparateKey: true,
		HelpURL:     "https://developers.facebook.com/docs/instagram-platform",
		Notes: "Not supported, and this preset exists to say so rather than to be used. Instagram publishes no Live " +
			"broadcast API — its platform covers messaging, content publishing and comments — and Live Producer's " +
			"RTMP option was withdrawn for most accounts. If your account is one of the few that still has it, the " +
			"server URL and key come from Live Producer and change every broadcast; otherwise there is nothing to " +
			"paste here and no amount of configuration will change that.",
		Aliases: []string{"meta", "ig"},
	},
	{
		ID: "tiktok", Name: "TikTok LIVE", Group: GroupMajor,
		Transport: PresetRTMP, Kind: DestRTMP,
		SeparateKey: true,
		Notes: "TikTok issues the server URL and key per broadcast, and only to accounts granted LIVE access. Create " +
			"the stream in TikTok LIVE Studio or LIVE Center and copy both fields from there.",
	},

	// ------------------------------------------------------- video platforms
	{
		ID: "x", Name: "X (Twitter) Live", Group: GroupVideo,
		Video: &VideoGuidance{
			Width: 1920, Height: 1080, FPS: 60, KbpsMin: 12000, KbpsMax: 12000, GOPSeconds: 3,
			Note:   "Live Studio's figures. X's older Media Studio Producer page is still published and disagrees materially (9000 recommended, 720p60), so no maximum is offered until the two reconcile. X ignores variants whose codec or bitrate it does not accept.",
			Source: "https://help.x.com/en/using-x/live-studio", Checked: "2026-08-06",
		},
		Transport: PresetRTMPS, Kind: DestRTMP,
		SeparateKey: true,
		Notes: "Manual key only. X's API covers posts, users, media and the post firehose — \"streaming\" in its " +
			"documentation means streaming posts, not ingesting video — so there is no documented endpoint for " +
			"polyemesis to fetch an ingest from, and none is planned. Create the source in X's own producer tooling " +
			"and copy the server URL and key from there.",
		Aliases: []string{"twitter", "periscope"},
	},
	{
		ID: "linkedin", Name: "LinkedIn Live", Group: GroupVideo,
		Video: &VideoGuidance{
			Width: 1280, Height: 720, FPS: 30, KbpsMin: 3500, KbpsMax: 6000, GOPSeconds: 2,
			Note:   "720p is LinkedIn's recommendation and 1080p its maximum. Published as guidance for cloud services rather than for a direct encoder, and the page carries only a relative date, so it will drift silently.",
			Source: "https://www.linkedin.com/help/linkedin/answer/a567498/", Checked: "2026-08-06",
		},
		Transport: PresetRTMPS, Kind: DestRTMP,
		SeparateKey: true,
		Notes: "LinkedIn issues the ingest URL and key per event, either from LinkedIn's own event creation flow or " +
			"from the approved streaming tool linked to the page. Copy both from the event.",
	},
	{
		ID: "trovo", Name: "Trovo", Group: GroupVideo,
		Video: &VideoGuidance{
			Width: 1920, Height: 1080, FPS: 30, KbpsMin: 4000, KbpsMax: 6000, GOPSeconds: 2,
			Note:   "Above 6000 kbps is subscribers only. Trovo's own page is undated and omits the keyframe interval, audio codec and audio bitrate; the 2s keyframe here is from OBS's services.json, in which Trovo maintains its own entry. OBS carries a 9000 kbps ceiling against Trovo's published 6000-for-non-subscribers.",
			Source: "https://support.trovo.live/category/1/article/778", Checked: "2026-08-06",
		},
		Transport: PresetRTMP, Kind: DestRTMP, Platform: PlatformTrovo,
		SeparateKey: true,
		// No HelpURL: Trovo's stream settings live behind a login and this file
		// has no verified public address for them. An invented one is worse
		// than none — the note below says where to look.
		//
		// KEEP THIS IN STEP WITH oauth.PlatformCapabilities()'s trovo row, for
		// the reason the kick preset above gives: that note went on telling
		// operators to copy a key polyemesis had started fetching for them.
		//
		// The split here is the unusual part and the note has to carry it. The
		// KEY is fetched from a connected account; the ingest URL is not, and
		// cannot be — Trovo issues the hostname per region and publishes it
		// only in the creator dashboard, so there is nothing to prefill and
		// nothing to look up. That is one field copied once, not per broadcast.
		Notes: "Connect a Trovo account in Settings → Platform credentials and polyemesis fetches the stream " +
			"key itself, over the channel_details_self scope. The server URL is yours to supply either way: " +
			"Trovo's ingest hostname varies by region and appears nowhere in its API, so copy it once from " +
			"the Trovo creator dashboard → Stream. Refreshing the key afterwards leaves that URL alone.",
	},
	{
		ID: "dlive", Name: "DLive", Group: GroupVideo,
		Transport: PresetRTMP, Kind: DestRTMP,
		SeparateKey: true,
		Notes: "Copy the server URL and stream key from DLive → Dashboard → Stream settings. Manual only, and likely " +
			"to stay that way: DLive's developer portal at dev.dlive.tv no longer resolves, so there is nothing " +
			"published to integrate against.",
	},
	{
		ID: "rumble", Name: "Rumble", Group: GroupVideo,
		Video: &VideoGuidance{
			Width: 1920, Height: 1080, FPS: 60, KbpsMin: 4000, KbpsMax: 6000, GOPSeconds: 2,
			Note:   "8000 kbps is Rumble's stated ceiling. Its language above the recommended range is degradation, not refusal.",
			Source: "https://rumble.support/help/livestream-settings", Checked: "2026-08-06",
		},
		Transport: PresetRTMP, Kind: DestRTMP,
		SeparateKey: true,
		Notes: "Manual key only. Rumble Studio issues an ingest URL and key per stream — set the stream up there and " +
			"copy both fields from its RTMP details. Rumble's API page (rumble.com/account/api) is behind a login " +
			"wall with nothing published, so there is no integration to build against.",
	},
	{
		ID: "odysee", Name: "Odysee", Group: GroupVideo,
		Transport: PresetRTMP, Kind: DestRTMP,
		SeparateKey: true,
		Notes: "Odysee issues a livestream ingest URL and key from the livestream setup on your channel. Copy both " +
			"from there.",
		Aliases: []string{"lbry"},
	},
	{
		ID: "vimeo", Name: "Vimeo Livestream", Group: GroupVideo,
		Transport: PresetRTMPS, Kind: DestRTMP,
		SeparateKey: true,
		// KEEP THIS IN STEP WITH oauth.PlatformCapabilities()'s vimeo row, the
		// same way the kick preset above asks to be. The key really is still
		// pasted -- Platform being set here does NOT mean polyemesis fetches
		// one, and that is the misreading this note exists to head off.
		Platform: PlatformVimeo,
		Notes: "Vimeo issues an RTMPS URL and key per live event. Open the event in Vimeo and copy the server URL " +
			"and stream key from its setup panel — connecting a Vimeo account does not change that, because " +
			"Vimeo's live API is available only to Vimeo Enterprise customers and a key belongs to an event. " +
			"Connect one anyway if you have an account: polyemesis asks the live API whether YOURS reaches it " +
			"and tells you at connect time, rather than letting the refusal turn up mid-broadcast. Create a " +
			"RECURRING event — Vimeo is deprecating one-time live events and recommends avoiding them.",
	},
	{
		ID: "dailymotion", Name: "Dailymotion", Group: GroupVideo,
		Video: &VideoGuidance{
			Width: 1920, Height: 1080, FPS: 60, KbpsMin: 10000, KbpsMax: 10000, GOPSeconds: 2,
			Note:   "H.264 High profile only, and live streaming is gated behind a paid plan. Dailymotion's 1080p figure is a steep jump from its 720p one (2500 kbps); reproduced as published.",
			Source: "https://faq.dailymotion.com/hc/en-us/articles/203655666-Encoding-parameters", Checked: "2026-08-06",
		},
		Transport: PresetRTMP, Kind: DestRTMP,
		SeparateKey: true,
		Notes: "Dailymotion Studio issues an ingest URL and key per live video. Copy both from the live video's " +
			"settings.",
	},

	// ----------------------------------------------------------- self-hosted
	{
		ID: "peertube", Name: "PeerTube", Group: GroupSelfHosted,
		Transport: PresetRTMP, Kind: DestRTMP,
		URL:         "rtmp://{peertube-host}:1935/live",
		SeparateKey: true,
		HelpURL:     "https://docs.joinpeertube.org/",
		Notes: "Replace {peertube-host} with your instance's hostname and paste the live stream key from the video's " +
			"Live settings. 1935 is the default RTMP port; an instance with RTMPS enabled listens on its own port, " +
			"so check that instance's live configuration.",
	},
	{
		ID: "owncast", Name: "Owncast", Group: GroupSelfHosted,
		Video: &VideoGuidance{
			Width: 1920, Height: 1080, FPS: 60, KbpsMin: 5000, KbpsMax: 5000, GOPSeconds: 2,
			Note:   "Owncast asks for an explicit 2s keyframe interval rather than auto. It does not re-encode audio, so whatever you send is what viewers get.",
			Source: "https://owncast.online/docs/broadcasting/", Checked: "2026-08-06",
		},
		Transport: PresetRTMP, Kind: DestRTMP,
		URL:         "rtmp://{owncast-host}:1935/live",
		SeparateKey: true,
		HelpURL:     "https://owncast.online/docs/broadcasting/",
		Notes: "Replace {owncast-host} with your Owncast server. The stream key is the one configured in Owncast's " +
			"admin under Server Setup, where the RTMP port can also be changed from the default 1935.",
	},
	{
		ID: "wowza-engine", Name: "Wowza Streaming Engine (self-hosted)", Group: GroupSelfHosted,
		Transport: PresetRTMP, Kind: DestRTMP,
		URL:         "rtmp://{wowza-host}:1935/{application}",
		SeparateKey: true,
		Notes: "Replace {wowza-host} and {application} with your Streaming Engine host and application name, and put " +
			"the stream name in the stream key field. For the hosted product use the Wowza Video preset instead.",
	},

	// ---------------------------------------------------------- CDN & cloud
	{
		ID: "cloudflare-stream", Name: "Cloudflare Stream", Group: GroupCloud,
		Transport: PresetRTMPS, Kind: DestRTMP,
		URL:         "rtmps://live.cloudflare.com:443/live/",
		SeparateKey: true,
		HelpURL:     "https://developers.cloudflare.com/stream/stream-live/",
		Notes: "Create a live input in Cloudflare Stream and paste its stream key. The dashboard shows the exact " +
			"RTMPS URL beside the key — use that if it differs from the one prefilled here. The same input also " +
			"accepts SRT and WHIP.",
		Aliases: []string{"cf"},
	},
	{
		ID: "mux", Name: "Mux Video", Group: GroupCloud,
		Transport: PresetRTMPS, Kind: DestRTMP,
		URL:         "rtmps://global-live.mux.com:443/app",
		SeparateKey: true,
		HelpURL:     "https://docs.mux.com/guides/video/stream-live-to-mux",
		Notes: "Create a live stream in Mux and paste its stream key. Mux publishes plain-RTMP and SRT endpoints for " +
			"the same stream; the dashboard shows the exact URLs if you want one of those instead.",
	},
	{
		ID: "aws-ivs", Name: "AWS IVS", Group: GroupCloud,
		Transport: PresetRTMPS, Kind: DestRTMP,
		URL:         "rtmps://{ingest-endpoint}:443/app/",
		SeparateKey: true,
		HelpURL:     "https://docs.aws.amazon.com/ivs/",
		Notes: "Replace {ingest-endpoint} with the channel's ingest endpoint from the IVS console and paste the " +
			"channel's stream key. Each channel gets its own endpoint, so this template is not usable as-is.",
		Aliases: []string{"amazon", "interactive video service"},
	},
	{
		ID: "restream", Name: "Restream.io relay", Group: GroupCloud,
		Transport: PresetRTMP, Kind: DestRTMP,
		SeparateKey: true,
		HelpURL:     "https://support.restream.io/",
		Notes: "Restream assigns an ingest close to you, so there is no single hostname to prefill. Copy the server " +
			"URL and key from Restream → Stream with RTMP. Note that relaying through Restream re-encodes your " +
			"audio, which undoes per-destination track routing — send tracks straight to each platform where you can.",
	},
	{
		ID: "castr", Name: "Castr", Group: GroupCloud,
		Transport: PresetRTMP, Kind: DestRTMP,
		SeparateKey: true,
		Notes: "Castr issues an ingest URL and key per stream. Copy both from the stream's ingest panel in the Castr " +
			"dashboard.",
	},
	{
		ID: "livepush", Name: "Livepush", Group: GroupCloud,
		Transport: PresetRTMP, Kind: DestRTMP,
		SeparateKey: true,
		Notes:       "Copy the ingest URL and stream key from the Livepush dashboard for the channel you are pushing to.",
	},
	{
		ID: "boxcast", Name: "BoxCast", Group: GroupCloud,
		Transport: PresetRTMP, Kind: DestRTMP,
		SeparateKey: true,
		Notes: "BoxCast issues an ingest URL and key per broadcast source. Create the source in the BoxCast " +
			"dashboard and copy both fields.",
	},
	{
		ID: "wowza-video", Name: "Wowza Video (hosted)", Group: GroupCloud,
		Transport: PresetRTMPS, Kind: DestRTMP,
		SeparateKey: true,
		Notes: "Wowza Video issues a primary ingest URL and stream name per live stream, and may also require " +
			"publishing credentials. Copy them from the live stream's source connection details.",
	},
	{
		ID: "akamai-msl", Name: "Akamai Media Services Live", Group: GroupCloud,
		Transport: PresetRTMP, Kind: DestRTMP,
		SeparateKey: true,
		Notes: "Akamai issues per-stream entry point hostnames and a stream ID in Media Services Live, and they are " +
			"account-specific. Copy the primary entry point and stream ID from the stream's configuration in " +
			"Akamai Control Center.",
		Aliases: []string{"msl"},
	},
	{
		ID: "azure-media", Name: "Azure live events", Group: GroupCloud,
		Transport: PresetRTMP, Kind: DestRTMP,
		SeparateKey: false,
		Notes: "A live event issues its own ingest URL once created and started; copy it from the event in the Azure " +
			"portal — the key, where there is one, is already part of that URL. Azure Media Services was retired in " +
			"June 2024, so confirm which live product your subscription actually has before relying on this.",
		Aliases: []string{"microsoft", "ams"},
	},

	// -------------------------------------------------------------- generic
	{
		ID: "generic-rtmp", Name: "Generic RTMP", Group: GroupGeneric,
		Transport: PresetRTMP, Kind: DestRTMP,
		URL:         "rtmp://{host}/{application}",
		SeparateKey: true,
		Notes: "Any RTMP ingest. Replace {host} and {application}; the stream key is joined onto the URL with a " +
			"slash when the stream starts, so leave it out of the URL itself.",
	},
	{
		ID: "generic-rtmps", Name: "Generic RTMPS", Group: GroupGeneric,
		Transport: PresetRTMPS, Kind: DestRTMP,
		URL:         "rtmps://{host}:443/{application}",
		SeparateKey: true,
		Notes: "RTMP over TLS. Use it when the network blocks port 1935 or the receiver requires TLS. Otherwise " +
			"identical to the generic RTMP preset.",
	},
	{
		ID: "generic-srt", Name: "Generic SRT", Group: GroupGeneric,
		Transport: PresetSRT, Kind: DestSRT,
		URL:         "srt://{host}:{port}?streamid={streamid}",
		SeparateKey: false,
		Notes: "SRT carries everything in the URL, including the stream id, so there is no separate key field. Add " +
			"&passphrase=… and &pbkeylen=… if the receiver requires encryption.",
	},
	{
		ID: "generic-hls", Name: "Generic HLS push", Group: GroupGeneric,
		Transport: PresetHLS,
		Notes: "polyemesis cannot push HLS to a remote HTTP endpoint. It does serve HLS itself — point players at " +
			"this server's /hls path — and it can send RTMP or SRT to a packager that produces HLS for you. " +
			"Choosing this preset leaves the transport alone.",
		Aliases: []string{"http", "m3u8"},
	},
}

// DestinationPresets returns the catalogue in display order. The slice is
// copied because a caller that reorders or edits it would corrupt it for every
// later request out of the same process.
func DestinationPresets() []DestinationPreset {
	out := make([]DestinationPreset, len(destinationPresets))
	copy(out, destinationPresets)
	return out
}

// DestinationPresetByID looks one preset up. Unknown ids report false rather
// than erroring: a preset id arriving from an older or newer UI is a hint we
// can ignore, never a reason to refuse to save a destination.
func DestinationPresetByID(id string) (DestinationPreset, bool) {
	for _, p := range destinationPresets {
		if p.ID == id {
			return p, true
		}
	}
	return DestinationPreset{}, false
}

// DestinationPresetsForPlatform returns the catalogue entries backed by an
// integration, so the UI can show the OAuth affordance for a preset without
// hard-coding which ids those are.
func DestinationPresetsForPlatform(p Platform) []DestinationPreset {
	out := []DestinationPreset{}
	for _, preset := range destinationPresets {
		if preset.Platform == p {
			out = append(out, preset)
		}
	}
	return out
}

// MigratePlatformAccountScopeVer adds the scope-version column.
//
// Defaults to 0, which reads as "connected before this existed" rather than
// "out of date". That distinction matters: bumping every stored account to a
// stale version would show a reconnect prompt on every account an operator
// has, including the ones connected yesterday with the full scope set, and a
// prompt that is wrong the first time is a prompt nobody reads the second time.
//
// The API layer decides what a 0 means, by comparing the scopes actually
// granted against the ones the provider now asks for. See
// oauth.AccountNeedsReconnect.
func (d *DB) MigratePlatformAccountScopeVer() error {
	has, err := columnExists(d.sql, "platform_accounts", "scope_ver")
	if err != nil {
		return err
	}
	if has {
		return nil
	}
	_, err = d.sql.Exec(`ALTER TABLE platform_accounts ADD COLUMN scope_ver INTEGER NOT NULL DEFAULT 0`)
	return err
}
