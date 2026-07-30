package db

import (
	"database/sql"
	"errors"
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
func (d *DB) TakeOAuthState(state string) (Platform, string, error) {
	var (
		p        Platform
		verifier string
		created  int64
	)
	err := d.sql.QueryRow(`SELECT platform, verifier, created_at FROM oauth_states WHERE state = ?`, state).
		Scan(&p, &verifier, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", errors.New("unknown or already-used OAuth state")
	}
	if err != nil {
		return "", "", err
	}
	if _, err := d.sql.Exec(`DELETE FROM oauth_states WHERE state = ?`, state); err != nil {
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
		Transport: PresetRTMPS, Kind: DestRTMP, Platform: PlatformKick,
		URL:         "rtmps://fa723fc1b171.global-contribute.live-video.net",
		SeparateKey: true,
		HelpURL:     "https://kick.com/dashboard/settings/stream",
		Notes: "Kick is the one platform where the key stays manual: its public API exposes the channel, chat and " +
			"viewer counts but no stream key anywhere. Copy both the ingest URL and the key from Kick → Settings → " +
			"Stream. Connecting a Kick account in Settings → Platform credentials is still worth doing — it pushes " +
			"your title and category and reports viewer counts. Kick issues the ingest host per channel, so replace " +
			"the one prefilled here with yours if it differs.",
	},
	{
		ID: "facebook", Name: "Facebook Live", Group: GroupMajor,
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
		Transport: PresetRTMPS, Kind: DestRTMP,
		SeparateKey: true,
		Notes: "LinkedIn issues the ingest URL and key per event, either from LinkedIn's own event creation flow or " +
			"from the approved streaming tool linked to the page. Copy both from the event.",
	},
	{
		ID: "trovo", Name: "Trovo", Group: GroupVideo,
		Transport: PresetRTMP, Kind: DestRTMP,
		SeparateKey: true,
		Notes: "Copy the server URL and stream key from the Trovo creator dashboard → Stream. Trovo's ingest " +
			"hostname varies by region, so nothing is prefilled here.",
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
		Notes: "Vimeo issues an RTMPS URL and key per live event. Open the event in Vimeo and copy the server URL " +
			"and stream key from its setup panel.",
	},
	{
		ID: "dailymotion", Name: "Dailymotion", Group: GroupVideo,
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
