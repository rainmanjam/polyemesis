package oauth

// Cross-platform metadata push.
//
// The operator types a title, a description and a category once; polyemesis
// writes them to every connected account that can accept them. The capability
// is expressed as a second interface rather than as new methods on Provider,
// so a platform that cannot do this — or cannot do part of it — is simply
// absent from the list instead of contributing an error the user cannot act
// on. Twitch has no stream description and YouTube's "category" is a fixed
// enumeration while Twitch's is a game directory, so MetadataCaps describes
// per-platform reality and the UI renders it rather than pretending the three
// fields are universal.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// MetadataField names one thing a platform may accept.
type MetadataField string

const (
	FieldTitle       MetadataField = "title"
	FieldDescription MetadataField = "description"
	FieldCategory    MetadataField = "category"
	// Compliance fields. Separate from the three above because they describe
	// an obligation rather than a description -- see compliance.go.
	FieldPrivacy     MetadataField = "privacy"
	FieldMadeForKids MetadataField = "madeForKids"
	FieldLabels      MetadataField = "contentLabels"
)

// Metadata is what the operator typed once in the go-live composer. An empty
// field means "leave whatever the platform already has" — blanking a live
// title by accident is far worse than requiring a second, explicit edit.
type Metadata struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	// Category is a human name — "Gaming", "Just Chatting" — because the
	// numeric id every platform actually wants is something only that
	// platform's own console can tell you.
	Category string `json:"category"`
}

// Empty reports whether there is nothing to push.
func (m Metadata) Empty() bool {
	return strings.TrimSpace(m.Title) == "" &&
		strings.TrimSpace(m.Description) == "" &&
		strings.TrimSpace(m.Category) == ""
}

// Trimmed returns a copy with surrounding whitespace removed, which is what a
// paste from a script document invariably carries.
func (m Metadata) Trimmed() Metadata {
	return Metadata{
		Title:       strings.TrimSpace(m.Title),
		Description: strings.TrimSpace(m.Description),
		Category:    strings.TrimSpace(m.Category),
	}
}

// MetadataCaps describes what one platform will accept, so the composer can
// say "Twitch has no description" up front instead of reporting it as a
// failure afterwards.
type MetadataCaps struct {
	Fields []MetadataField `json:"fields"`
	// CategoryLabel is the platform's own word for the category field.
	CategoryLabel string `json:"categoryLabel,omitempty"`
	CategoryHint  string `json:"categoryHint,omitempty"`
	// TitleMax and DescriptionMax are the platform's documented hard limits.
	// Zero means "no published limit".
	TitleMax       int `json:"titleMax,omitempty"`
	DescriptionMax int `json:"descriptionMax,omitempty"`
	// Scope is the OAuth scope the write needs. It is reported, never
	// enforced: an account connected before this feature existed still has a
	// perfectly good token, and refusing to try would be a capability check
	// wrong in the restrictive direction. The platform's own 401 is the
	// authority, and PushMetadata turns it into advice naming this scope.
	Scope string `json:"scope,omitempty"`
}

// Accepts reports whether this platform can take the given field.
func (c MetadataCaps) Accepts(f MetadataField) bool {
	for _, have := range c.Fields {
		if have == f {
			return true
		}
	}
	return false
}

// MetadataResult is one platform's outcome. Partial success is the normal
// case — a title that lands and a category that does not is a real and common
// state — so this reports per field rather than as a boolean.
type MetadataResult struct {
	Applied []MetadataField `json:"applied"`
	// Skipped is what the caller asked for that this platform could not take,
	// with the reason in Warnings.
	Skipped []MetadataField `json:"skipped,omitempty"`
	// Target names what was actually edited, so the operator can tell which
	// of three scheduled broadcasts received the change.
	Target string `json:"target,omitempty"`
	// Category is the platform's own spelling of the category that matched.
	Category string   `json:"category,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// MetadataPusher is the optional capability. Discover it with MetadataFor;
// never type-assert Provider to it at a call site, because "absent" is a
// supported answer and has to be handled once.
type MetadataPusher interface {
	Provider
	MetadataCaps() MetadataCaps
	// PushMetadata writes what it can and reports what it did. It returns an
	// error only when nothing at all could be written.
	//
	// accountRef is the channel id recorded when the account was connected.
	// Passing it in rather than re-deriving it is what keeps a push to a
	// platform that identifies the channel by id — Twitch — down to the writes
	// themselves, on a code path the operator is running seconds before air.
	PushMetadata(ctx context.Context, clientID, accessToken, accountRef string, m Metadata) (*MetadataResult, error)
}

// MetadataFor returns the metadata capability for a platform, or false when
// that platform has none.
func MetadataFor(p db.Platform) (MetadataPusher, bool) {
	pr, ok := Providers()[p]
	if !ok {
		return nil, false
	}
	mp, ok := pr.(MetadataPusher)
	return mp, ok
}

// ------------------------------------------------------------------ transport

// statusError carries the HTTP status alongside the message so a caller can
// turn a 401 into advice about scopes rather than echoing a bare number.
type statusError struct {
	Status int
	URL    string
	Body   string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("%s returned %d: %s", e.URL, e.Status, e.Body)
}

// requestJSON performs an authenticated request with an arbitrary method.
// getJSON and postJSON cover the read and create paths; metadata writes are
// PUT and PATCH, and they need the status back rather than a flattened string.
func requestJSON(ctx context.Context, method, endpoint, accessToken string, payload any, headers map[string]string, out any) error {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &statusError{Status: resp.StatusCode, URL: endpoint, Body: snippet(raw)}
	}
	// A successful PATCH is a 204 on Helix; there is nothing to decode.
	if out == nil || len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// scopeAdvice turns an authorization failure into the one instruction that
// fixes it. An account connected before metadata push shipped holds a token
// that was never granted the write scope, and the platform's own wording for
// that ("insufficient authentication scopes") tells the operator nothing about
// where the button is.
func scopeAdvice(err error, platform db.Platform, scope string) error {
	se, ok := err.(*statusError)
	if !ok || scope == "" {
		return err
	}
	if se.Status != http.StatusUnauthorized && se.Status != http.StatusForbidden {
		return err
	}
	return fmt.Errorf("%s refused the write (%d). If this account was connected before "+
		"metadata push existed it never granted %q — disconnect and reconnect it in "+
		"Settings → Platforms. Platform said: %s",
		platform, se.Status, scope, se.Body)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// ------------------------------------------------------------------- YouTube

// ytAPIBase is a var so tests can point the whole provider at a stub. Nothing
// at runtime rewrites it.
var ytAPIBase = "https://www.googleapis.com/youtube/v3"

// ytCategoryRegion decides which localised category list we match names
// against. YouTube's assignable categories are the same set worldwide; the
// region only changes their spelling, and matching against the English names
// is what makes "Science & Technology" work for everyone.
const ytCategoryRegion = "US"

func (y *YouTube) MetadataCaps() MetadataCaps {
	return MetadataCaps{
		Fields:        []MetadataField{FieldTitle, FieldDescription, FieldCategory},
		CategoryLabel: "Category",
		CategoryHint:  "A YouTube video category, e.g. Gaming, Music, Science & Technology.",
		// YouTube's documented snippet limits.
		TitleMax:       100,
		DescriptionMax: 5000,
		Scope:          "https://www.googleapis.com/auth/youtube",
	}
}

type ytBroadcast struct {
	ID      string `json:"id"`
	Snippet struct {
		Title              string `json:"title"`
		Description        string `json:"description"`
		ScheduledStartTime string `json:"scheduledStartTime"`
	} `json:"snippet"`
	Status struct {
		LifeCycleStatus string `json:"lifeCycleStatus"`
	} `json:"status"`
}

// broadcastRank orders candidates: whatever is on air now, then whatever is
// scheduled, and never anything already finished. Editing the title of last
// week's stream because it happened to sort first would be the single most
// embarrassing outcome this feature has available to it.
func broadcastRank(status string) int {
	switch strings.ToLower(status) {
	case "live", "livestarting":
		return 0
	case "testing", "teststarting":
		return 1
	case "ready":
		return 2
	case "created":
		return 3
	default: // complete, revoked, anything YouTube adds later
		return -1
	}
}

// liveBroadcast picks the broadcast the operator means: the live one, else the
// soonest upcoming one.
func (y *YouTube) liveBroadcast(ctx context.Context, accessToken string) (*ytBroadcast, error) {
	var list struct {
		Items []ytBroadcast `json:"items"`
	}
	err := getJSON(ctx,
		ytAPIBase+"/liveBroadcasts?part=id,snippet,status&broadcastStatus=all&broadcastType=all&maxResults=50",
		accessToken, nil, &list)
	if err != nil {
		return nil, err
	}

	candidates := make([]ytBroadcast, 0, len(list.Items))
	for _, b := range list.Items {
		if broadcastRank(b.Status.LifeCycleStatus) >= 0 {
			candidates = append(candidates, b)
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("this channel has no live or upcoming broadcast to update; " +
			"schedule one (or go live) in YouTube Studio and push again")
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		ri, rj := broadcastRank(candidates[i].Status.LifeCycleStatus), broadcastRank(candidates[j].Status.LifeCycleStatus)
		if ri != rj {
			return ri < rj
		}
		// RFC 3339 sorts lexicographically, so the soonest start wins without
		// parsing. A broadcast with no start time sorts last.
		si, sj := candidates[i].Snippet.ScheduledStartTime, candidates[j].Snippet.ScheduledStartTime
		if (si == "") != (sj == "") {
			return sj == ""
		}
		return si < sj
	})
	b := candidates[0]
	return &b, nil
}

// PushMetadata ignores accountRef: the Live Streaming API scopes every call to
// the token's own channel, so there is nothing to address.
func (y *YouTube) PushMetadata(ctx context.Context, clientID, accessToken, accountRef string, m Metadata) (*MetadataResult, error) {
	m = m.Trimmed()
	b, err := y.liveBroadcast(ctx, accessToken)
	if err != nil {
		return nil, err
	}

	res := &MetadataResult{Target: b.Snippet.Title}

	if m.Title != "" || m.Description != "" {
		snip := map[string]any{
			"title":       firstNonEmpty(m.Title, b.Snippet.Title),
			"description": firstNonEmpty(m.Description, b.Snippet.Description),
		}
		// YouTube rejects a snippet update for a scheduled broadcast that omits
		// scheduledStartTime, so it is echoed back unchanged rather than left
		// out — this endpoint replaces the snippet, it does not merge it.
		if b.Snippet.ScheduledStartTime != "" {
			snip["scheduledStartTime"] = b.Snippet.ScheduledStartTime
		}
		err := requestJSON(ctx, http.MethodPut, ytAPIBase+"/liveBroadcasts?part=snippet",
			accessToken, map[string]any{"id": b.ID, "snippet": snip}, nil, nil)
		if err != nil {
			return nil, scopeAdvice(err, db.PlatformYouTube, y.MetadataCaps().Scope)
		}
		if m.Title != "" {
			res.Applied = append(res.Applied, FieldTitle)
			res.Target = m.Title
		}
		if m.Description != "" {
			res.Applied = append(res.Applied, FieldDescription)
		}
	}

	if m.Category != "" {
		name, err := y.setCategory(ctx, accessToken, b.ID, m.Category)
		if err != nil {
			// The title is already live at this point; reporting the whole push
			// as failed would send the operator back to re-type work that did
			// land. The category alone is what did not.
			res.Skipped = append(res.Skipped, FieldCategory)
			res.Warnings = append(res.Warnings, err.Error())
		} else {
			res.Applied = append(res.Applied, FieldCategory)
			res.Category = name
		}
	}
	return res, nil
}

type ytVideoSnippet struct {
	Title           string   `json:"title"`
	Description     string   `json:"description"`
	CategoryID      string   `json:"categoryId"`
	Tags            []string `json:"tags,omitempty"`
	DefaultLanguage string   `json:"defaultLanguage,omitempty"`
}

// setCategory resolves a human category name and writes it. A broadcast is a
// video, and the category lives on the video resource rather than on the
// broadcast, which is why this is a second round trip rather than one more
// field above.
func (y *YouTube) setCategory(ctx context.Context, accessToken, videoID, name string) (string, error) {
	id, resolved, err := y.categoryID(ctx, accessToken, name)
	if err != nil {
		return "", err
	}

	// videos.update replaces the whole snippet part, so the current one is read
	// back first: sending only a categoryId would silently erase the tags and
	// the title we just set.
	var current struct {
		Items []struct {
			Snippet ytVideoSnippet `json:"snippet"`
		} `json:"items"`
	}
	err = getJSON(ctx, ytAPIBase+"/videos?part=snippet&id="+url.QueryEscape(videoID), accessToken, nil, &current)
	if err != nil {
		return "", err
	}
	if len(current.Items) == 0 {
		return "", fmt.Errorf("YouTube did not return the broadcast's video, so its category was left alone")
	}
	snip := current.Items[0].Snippet
	snip.CategoryID = id

	err = requestJSON(ctx, http.MethodPut, ytAPIBase+"/videos?part=snippet", accessToken,
		map[string]any{"id": videoID, "snippet": snip}, nil, nil)
	if err != nil {
		return "", scopeAdvice(err, db.PlatformYouTube, y.MetadataCaps().Scope)
	}
	return resolved, nil
}

// categoryID matches a typed name against the assignable categories. Only
// assignable ones are offered: YouTube still lists retired categories, and
// setting one is rejected at write time with a message that blames the video.
func (y *YouTube) categoryID(ctx context.Context, accessToken, name string) (id, resolved string, err error) {
	var list struct {
		Items []struct {
			ID      string `json:"id"`
			Snippet struct {
				Title      string `json:"title"`
				Assignable bool   `json:"assignable"`
			} `json:"snippet"`
		} `json:"items"`
	}
	err = getJSON(ctx, ytAPIBase+"/videoCategories?part=snippet&regionCode="+ytCategoryRegion, accessToken, nil, &list)
	if err != nil {
		return "", "", err
	}

	want := normaliseCategory(name)
	var assignable []string
	for _, c := range list.Items {
		if !c.Snippet.Assignable {
			continue
		}
		assignable = append(assignable, c.Snippet.Title)
		if normaliseCategory(c.Snippet.Title) == want {
			return c.ID, c.Snippet.Title, nil
		}
	}
	sort.Strings(assignable)
	return "", "", fmt.Errorf("YouTube has no category called %q. Valid ones are: %s",
		name, strings.Join(assignable, ", "))
}

// normaliseCategory makes name matching survive the ways people actually type
// a category: "science and technology" for "Science & Technology", or a
// stray double space out of a paste.
func normaliseCategory(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "&", " and ")
	return strings.Join(strings.Fields(s), " ")
}

// -------------------------------------------------------------------- Twitch

var twitchHelixBase = "https://api.twitch.tv/helix"

func (t *Twitch) MetadataCaps() MetadataCaps {
	return MetadataCaps{
		// No description: Twitch streams have a title and a category, and
		// nothing the Helix channel resource accepts is a description. Saying
		// so here is what keeps it out of the failure list.
		Fields:        []MetadataField{FieldTitle, FieldCategory},
		CategoryLabel: "Category",
		CategoryHint:  "A Twitch category or game, e.g. Just Chatting, Software and Game Development.",
		TitleMax:      140,
		Scope:         "channel:manage:broadcast",
	}
}

func (t *Twitch) PushMetadata(ctx context.Context, clientID, accessToken, accountRef string, m Metadata) (*MetadataResult, error) {
	m = m.Trimmed()
	if accountRef == "" {
		return nil, fmt.Errorf("this Twitch account has no broadcaster id recorded; reconnect it in Settings → Platforms")
	}

	res := &MetadataResult{}
	body := map[string]any{}

	if m.Title != "" {
		body["title"] = m.Title
	}
	if m.Description != "" {
		res.Skipped = append(res.Skipped, FieldDescription)
	}

	// Resolved before the write so that a category we cannot find costs the
	// operator a warning rather than the title change.
	var gameName string
	if m.Category != "" {
		id, name, err := t.gameID(ctx, clientID, accessToken, m.Category)
		if err != nil {
			res.Skipped = append(res.Skipped, FieldCategory)
			res.Warnings = append(res.Warnings, err.Error())
		} else {
			body["game_id"] = id
			gameName = name
		}
	}

	if len(body) == 0 {
		return res, nil
	}
	err := requestJSON(ctx, http.MethodPatch,
		twitchHelixBase+"/channels?broadcaster_id="+url.QueryEscape(accountRef),
		accessToken, body, helixHeaders(clientID), nil)
	if err != nil {
		return nil, scopeAdvice(err, db.PlatformTwitch, t.MetadataCaps().Scope)
	}

	if _, ok := body["title"]; ok {
		res.Applied = append(res.Applied, FieldTitle)
	}
	if gameName != "" {
		res.Applied = append(res.Applied, FieldCategory)
		res.Category = gameName
	}
	return res, nil
}

type twitchGame struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// gameID turns a typed category into Helix's numeric game_id. The exact-name
// endpoint is tried first because it is unambiguous; the search endpoint is
// the fallback that forgives a near miss like "software and game dev".
func (t *Twitch) gameID(ctx context.Context, clientID, accessToken, name string) (id, resolved string, err error) {
	var exact struct {
		Data []twitchGame `json:"data"`
	}
	err = getJSON(ctx, twitchHelixBase+"/games?name="+url.QueryEscape(name),
		accessToken, helixHeaders(clientID), &exact)
	if err != nil {
		return "", "", err
	}
	if len(exact.Data) > 0 && exact.Data[0].ID != "" {
		return exact.Data[0].ID, exact.Data[0].Name, nil
	}

	var found struct {
		Data []twitchGame `json:"data"`
	}
	err = getJSON(ctx, twitchHelixBase+"/search/categories?first=20&query="+url.QueryEscape(name),
		accessToken, helixHeaders(clientID), &found)
	if err != nil {
		return "", "", err
	}
	want := normaliseCategory(name)
	for _, g := range found.Data {
		if normaliseCategory(g.Name) == want {
			return g.ID, g.Name, nil
		}
	}
	if len(found.Data) > 0 {
		names := make([]string, 0, len(found.Data))
		for _, g := range found.Data {
			names = append(names, g.Name)
		}
		if len(names) > 5 {
			names = names[:5]
		}
		return "", "", fmt.Errorf("Twitch has no category called %q. Did you mean: %s?",
			name, strings.Join(names, ", "))
	}
	return "", "", fmt.Errorf("Twitch has no category matching %q", name)
}
