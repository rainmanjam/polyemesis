package oauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// YouTube's scheduled broadcasts: create one before the show, and move it when
// the schedule moves.
//
// EVERY ENDPOINT, PARAMETER AND REFUSAL BELOW IS IN
// docs/evidence/platform-lifecycle-apis-2026-08-16.md with a dated citation.
// Nothing here was written from memory of the YouTube API, and the one claim
// this file leans hardest on is quoted rather than paraphrased -- see
// RescheduleBroadcast.
//
// THERE IS NO Create METHOD HERE ON PURPOSE, and that is not a gap. Creating a
// scheduled broadcast is TargetedProvider.IngestFor with
// IngestOptions.ScheduledFor set, which is the shape ScheduledBroadcaster's own
// doc comment records: "a Create on this interface would be a second mechanism
// for one concept, which is exactly how endpoints.go records the graphBase seam
// growing up beside WithBaseURL and covering one endpoint out of thirteen."
// A YouTube-only Create would have been that seam a second time, on the one
// call whose failure mode is a public event page nothing in this database names.
//
// Both halves of the pair arrive together for the reason that comment gives: a
// platform holding one half can pre-announce nothing.
var (
	_ TargetedProvider     = (*YouTube)(nil)
	_ ScheduledBroadcaster = (*YouTube)(nil)
)

// ytPlaceholderTitle is what a scheduled broadcast is called until a title push
// replaces it, and it exists because snippet.title is REQUIRED at create.
// liveBroadcasts/insert, verbatim: "You must specify a value for these
// properties: snippet.title / snippet.scheduledStartTime / status.privacyStatus"
// (read 2026-08-16). There is no way to create a broadcast without naming it.
//
// IngestOptions carries no title, so there is nothing better to send TODAY:
// filling that in means a field on IngestOptions and a writer for it in
// internal/api, and internal/api belongs to the round that wires lifecycle to
// go-live. A placeholder that says what the thing is beats one that says
// "polyemesis" to a viewer, and PushMetadata replaces it -- liveBroadcast()
// ranks a created/ready broadcast as the one the operator means, so the title
// push at go-live lands on exactly this object.
//
// KNOWN AND REPORTED, not hidden: until that wiring exists, a show pre-announced
// days ahead carries this name on a public page for those days.
const ytPlaceholderTitle = "Scheduled stream"

// ytScheduledPrivacy is the privacyStatus a pre-announced broadcast is created
// with, and public is the only value that makes the feature mean anything: a
// private scheduled broadcast announces the show to nobody, which is the whole
// thing being asked for. IngestOptions.ScheduledFor's own comment puts it as
// "a scheduled one is a PUBLIC event page from the moment it is created."
//
// IT IS NOT READ FROM THE OPERATOR'S CHOICE, and that is a real gap rather than
// a decision. db.Compliance.Privacy is YouTube's privacyStatus and polyemesis
// already stores it, but IngestOptions carries only Facebook's FacebookPrivacy
// -- so a destination set to private gets a public event page until
// PushCompliance runs at go-live. Reinterpreting FacebookPrivacy as a YouTube
// value would be worse than the gap: EVERYONE and SELF are Facebook's audience
// words, they are not YouTube's three, and a mapping invented here would look
// like the operator's choice being honoured while being something else. The
// caller is the layer that knows the destination's compliance, so the gate
// belongs there.
const ytScheduledPrivacy = "public"

// ------------------------------------------------------------ targets

// Targets returns the one place this token may publish.
//
// YOU ARE READING THE ONE-ELEMENT CASE OF A MULTI-TARGET INTERFACE, which looks
// odd until you ask what the interface is for. TargetedProvider's value is that
// IngestFor takes a targetRef and returns the BROADCAST behind the ingest --
// Provider.Ingest has nowhere to put an id -- and that is exactly what a
// scheduled YouTube broadcast needs. One connected Google account addresses one
// channel, so this answers with one entry rather than pretending to a choice
// the token does not have.
func (y *YouTube) Targets(ctx context.Context, clientID, accessToken string) ([]BroadcastTarget, error) {
	acct, err := y.Account(ctx, clientID, accessToken)
	if err != nil {
		return nil, err
	}
	return []BroadcastTarget{{
		Ref:  acct.Ref,
		Kind: "channel",
		Name: acct.Name,
	}}, nil
}

// AccountFor identifies the chosen target, which for YouTube can only be the
// token's own channel.
//
// A ref naming a DIFFERENT channel is refused rather than ignored. Every YouTube
// call polyemesis makes is scoped to whatever channel the token belongs to --
// there is no addressing parameter to get it wrong with -- so a stored ref that
// no longer matches means the operator reconnected a different Google account
// onto the same destination. Ignoring it would publish the next show to the new
// channel while every screen still named the old one, and the stream key would
// work perfectly the whole time.
func (y *YouTube) AccountFor(ctx context.Context, clientID, accessToken, targetRef string) (*Account, error) {
	acct, err := y.Account(ctx, clientID, accessToken)
	if err != nil {
		return nil, err
	}
	ref := strings.TrimSpace(targetRef)
	// Empty means the default, which is the same channel. See TargetedProvider.
	if ref != "" && ref != acct.Ref {
		return nil, fmt.Errorf("this Google account is connected to YouTube channel %s (%s), "+
			"not to %s, which is the channel this destination was set up against; "+
			"reconnect the right account, or point the destination at the new channel",
			acct.Ref, acct.Name, ref)
	}
	return acct, nil
}

// ------------------------------------------------------- create and bind

// IngestFor returns the ingest THIS DESTINATION publishes to, and -- when
// opts.ScheduledFor is set -- creates the broadcast that will carry it and
// binds the two together.
//
// "THIS DESTINATION'S" RATHER THAN "THE CHANNEL'S", and that distinction is the
// difference between three simultaneous YouTube destinations and as many as the
// channel allows. streamFor honours opts.DedicatedIngest and opts.HeldKey; read
// its comment for which stream comes back and why. The scheduled path below
// then binds to whatever it returned, so a destination with its own stream gets
// a broadcast bound to its own stream -- which is what makes it a separate
// ingestion source rather than a co-tenant of one.
//
// THE ZERO ScheduledFor PATH CREATES NOTHING, and that is deliberate to the
// point of being the reason this method can exist at all. internal/api routes
// EVERY go-live through TargetsFor/IngestFor when a platform has the capability,
// so making YouTube a TargetedProvider re-points the existing go-live path at
// this function. Live-now therefore does exactly what Provider.Ingest did: it
// returns the channel's reusable stream with no broadcast id, which is the same
// shape internal/api's own fallback builds for a platform with no broadcast
// object. Creating a broadcast here instead would need a scheduledStartTime for
// a show starting now -- an invented value on a required field -- and would put
// a second broadcast on the channel every time somebody pressed "refresh key".
//
// THREE CALLS FOR THE SCHEDULED PATH, in this order, because each needs the one
// before it:
//
//  1. liveStreams.list/insert, for the ingest AND the stream id (streamFor).
//  2. liveBroadcasts.insert, which yields the broadcast id.
//  3. liveBroadcasts.bind, which joins them. Without it the broadcast exists,
//     the encoder publishes, and the two never meet.
//
// contentDetails is NOT sent. enableAutoStart would make this broadcast go live
// on its own when bytes arrive, and deciding when a show starts is the go-live
// round's business, not this one's -- it is the one change here that could start
// or stop a broadcast. YouTube's defaults stand until something asks otherwise.
//
// Backups are not filled in either, though the liveStream resource carries a
// backupIngestionAddress. Populating Broadcast.Backups would write
// Destination.BackupURL for every YouTube destination on the next key refresh,
// which is a behaviour change to go-live wearing the clothes of a scheduling
// change.
func (y *YouTube) IngestFor(ctx context.Context, clientID, accessToken, targetRef string, opts IngestOptions) (*Broadcast, error) {
	acct, err := y.AccountFor(ctx, clientID, accessToken, targetRef)
	if err != nil {
		return nil, err
	}
	stream, err := y.streamFor(ctx, accessToken, opts)
	if err != nil {
		return nil, err
	}
	b := &Broadcast{
		Target: acct.Ref,
		Ingest: Ingest{
			URL: stream.CDN.IngestionInfo.IngestionAddress,
			Key: stream.CDN.IngestionInfo.StreamName,
		},
	}
	if opts.ScheduledFor.IsZero() {
		return b, nil
	}

	created, err := y.createScheduledBroadcast(ctx, accessToken, opts.ScheduledFor)
	if err != nil {
		return nil, err
	}
	if err := y.bindBroadcast(ctx, accessToken, created.ID, stream.ID); err != nil {
		// THE BROADCAST IS DELETED, NOT NAMED IN A MESSAGE, AND THE DIFFERENCE
		// IS ~288 PUBLIC EVENT PAGES A DAY.
		//
		// YouTube's create is three calls and Facebook's is one, which is the
		// whole problem. internal/api/preannounce.go was written against the
		// single-POST shape: any IngestFor error means nothing was created, so
		// announceOne calls Forget and the next sweep tries again. Here the
		// broadcast EXISTS by the time bind fails -- a real, public event page
		// on the operator's channel -- so "try again in five minutes" mints
		// another one. liveBroadcastBindingNotAllowed is state-gated and
		// persistent, so it does not resolve on its own; the sweep runs all day.
		//
		// It is silent, too. announceOne logs only when its failure counter
		// first reaches 1, and that counter is not cleared on this path, so
		// orphans two onward produce no log line at all.
		//
		// An earlier version of this function named the id in the error and
		// asked the operator to delete it, arguing a delete was "a second write
		// whose own failure would leave the same orphan plus a misleading
		// error". That is true of the failure case and misses the common one:
		// the delete usually SUCCEEDS, and then there is no orphan and no
		// instruction to follow. Best case beats worst case here because the
		// worst case is exactly what we already had.
		if delErr := y.deleteBroadcast(ctx, accessToken, created.ID); delErr == nil {
			return nil, fmt.Errorf("YouTube would not bind scheduled broadcast %s to the "+
				"channel's stream, so it was deleted rather than left as an event page for a "+
				"show that publishes elsewhere: %w", created.ID, err)
		}
		// The delete failed too. NOW the id belongs in the message, because it
		// is the only remaining way anyone learns the page is there.
		return nil, fmt.Errorf("YouTube created scheduled broadcast %s, would not bind it to "+
			"the channel's stream, and would not delete it either. Delete broadcast %s in "+
			"YouTube Studio: it is a public event page for a show that goes out elsewhere, "+
			"and the next attempt will create another: %w",
			created.ID, created.ID, err)
	}
	b.ID = created.ID
	return b, nil
}

// createScheduledBroadcast is liveBroadcasts.insert with the three required
// properties and nothing else.
//
// part=snippet,status names exactly the parts this body carries. That is not
// tidiness: liveBroadcasts is destructive BY PART -- see the traps at the top of
// youtube_broadcast.go -- so a part named without its fields supplied is a part
// reset to defaults. Naming contentDetails here would ask YouTube to default
// every toggle on a broadcast that has none yet.
func (y *YouTube) createScheduledBroadcast(ctx context.Context, accessToken string, at time.Time) (*ytBroadcast, error) {
	var created ytBroadcast
	// RFC 3339 in UTC. The resource types scheduledStartTime as a datetime and
	// the errors page lists invalidValue/invalidScheduledStartTime for a value
	// it will not read; sending the caller's own zone would put the parsing of a
	// required field at the mercy of the server's locale for no gain.
	payload := map[string]any{
		"snippet": map[string]any{
			"title":              ytPlaceholderTitle,
			"scheduledStartTime": at.UTC().Format(time.RFC3339),
		},
		"status": map[string]any{"privacyStatus": ytScheduledPrivacy},
	}
	err := postJSON(ctx, y.apiEndpoint()+ytBroadcastsPath+"?part=snippet,status",
		accessToken, payload, nil, &created)
	if err != nil {
		return nil, ytBroadcastCreateAdvice(err)
	}
	if strings.TrimSpace(created.ID) == "" {
		// A create that answers 200 with no id has left something on the channel
		// that nothing can address. Said out loud rather than returned as an
		// empty Broadcast, which would read as "no broadcast was made".
		return nil, fmt.Errorf("YouTube accepted the scheduled broadcast but returned no id, " +
			"so nothing here can move it, bind it or end it; check YouTube Studio before " +
			"scheduling this show again")
	}
	return &created, nil
}

// bindBroadcast joins a broadcast to the stream that feeds it.
//
// The parameters are QUERY parameters and the body is empty -- bind is not a
// resource write. streamId is documented as optional precisely because omitting
// it REMOVES a binding, so a bug that dropped it here would silently unbind
// rather than fail.
func (y *YouTube) bindBroadcast(ctx context.Context, accessToken, broadcastID, streamID string) error {
	if strings.TrimSpace(streamID) == "" {
		// Never send bind without a streamId: that is the documented spelling of
		// "remove the binding", so the request would succeed and do the opposite
		// of what this call is for.
		return fmt.Errorf("YouTube returned no id for the channel's ingest stream, and binding " +
			"a broadcast without one is how a binding is REMOVED rather than made")
	}
	q := url.Values{}
	q.Set("id", broadcastID)
	q.Set("streamId", streamID)
	// part=id AND NOTHING MORE, because this call's response is discarded --
	// requestJSON is given a nil out. An earlier version sent
	// "id,contentDetails", which asks YouTube to compose a part of a body
	// nobody reads.
	//
	// NOT EVIDENCED, AND SAYING SO IS THE POINT. The evidence file documents
	// this endpoint, streamId's optionality, the cardinality rule and
	// liveBroadcastBindingNotAllowed -- it does NOT record that bind takes a
	// part parameter at all, nor any accepted value. "id" is used because it is
	// the one part value the file does evidence for a sibling resource
	// (part=id,snippet,status on liveBroadcasts.list) and because it is the
	// smallest thing that could satisfy a required parameter.
	// Resolve by: one live bind, with and without part, recording both answers.
	q.Set("part", "id")
	// requestJSON with a nil payload sends no body and no Content-Type, which is
	// what a method whose whole input is in the query string should send.
	return requestJSON(ctx, http.MethodPost,
		y.apiEndpoint()+ytBindPath+"?"+q.Encode(), accessToken, nil, nil, nil)
}

// ------------------------------------------------- ScheduledBroadcaster

// ScheduleHorizon reports that YOUTUBE PUBLISHES NO BOUND.
//
// Facebook returns seven days because Meta documents seven days. YouTube
// documents nothing of the kind: liveBroadcasts/insert requires
// snippet.scheduledStartTime and says nothing about how far ahead it may point,
// and the live errors page carries invalidScheduledStartTime with no limit
// attached (both read 2026-08-16). A number invented to fill that silence would
// be indistinguishable in this code from Facebook's documented one, and it would
// fail by SKIPPING -- the caller's gate drops an out-of-range occurrence without
// an error -- so a guess of 30 days would quietly stop pre-announcing a show
// booked five weeks out and nothing anywhere would say why.
//
// See ScheduleHorizonUnbounded for why the sentinel is the maximum Duration
// rather than zero.
func (y *YouTube) ScheduleHorizon() time.Duration { return ScheduleHorizonUnbounded }

// RescheduleBroadcast moves an already-created broadcast to a new start time.
//
// READ-THEN-WRITE, NEVER A BARE UPDATE, and both halves of that are forced:
// liveBroadcasts.update requires id, snippet.scheduledStartTime,
// contentDetails.monitorStream.enableMonitorStream and .broadcastStreamDelayMs
// on EVERY call, and it is destructive by part -- a part sent without one of its
// fields reverts that field to its default. Moving a start time by writing only
// the start time would therefore blank the broadcast's title, its description
// and its monitor-stream settings, and answer 200. writeBroadcastParts already
// carries every field of every part it sends, so the move goes through it rather
// than beside it: a second update path would be the second mechanism for one
// concept that this package keeps paying for.
//
// THE READ IS BY id, WHICH IS A FILTER. liveBroadcasts.list heads its filter
// group "Filters (specify exactly one of the following parameters)" over
// broadcastStatus, id and mine, so this request carries id ALONE -- adding mine
// to prove ownership, or broadcastStatus to prove liveness, is not a stricter
// query but an invalid one. broadcastType is absent for the same reason it is
// mandatory in Stats: it is documented for requests that "set the mine parameter
// to true or that use the broadcastStatus parameter", and this is neither.
func (y *YouTube) RescheduleBroadcast(ctx context.Context, accessToken, broadcastID string, at time.Time) error {
	// Refused before any call, for the reason the interface gives: an empty id
	// against a list filter is not "no results", it is an unfiltered list, and
	// the update that followed would move whatever happened to come back first.
	if strings.TrimSpace(broadcastID) == "" {
		return fmt.Errorf("reschedule: no YouTube broadcast id")
	}
	b, err := y.broadcastByID(ctx, accessToken, broadcastID)
	if err != nil {
		return err
	}
	// THE OWNERSHIP CHECK, and it costs a call. liveBroadcasts.list says "owned
	// by the authenticated user" exactly once, in the `mine` row; nothing on the
	// page scopes the `id` filter to the caller, so whether this read can return
	// somebody else's broadcast is UNVERIFIED. What it protects against is
	// concrete rather than theoretical: an operator who reconnects a different
	// Google account to the same destination leaves a stored broadcast id
	// pointing at the OLD channel, and moving that would rewrite a broadcast on
	// a channel this token has no business touching. Without the check the
	// symptom is a bare 403 from the update, which reads as a scope problem.
	//
	// One extra channels.list per reschedule, against a quota YouTube does not
	// publish a cost for. A reschedule happens when a show's time changes, not
	// on a poll, so the price is a call per moved show.
	if cid := strings.TrimSpace(b.Snippet.ChannelID); cid != "" {
		// An empty clientID because YouTube's Account ignores it: the Data API
		// scopes channels?mine=true to the bearer token, and there is nothing a
		// developer app id would add to the request.
		acct, aerr := y.Account(ctx, "", accessToken)
		if aerr != nil {
			return aerr
		}
		if cid != acct.Ref {
			return fmt.Errorf("broadcast %s belongs to YouTube channel %s, but this "+
				"connection is to channel %s (%s); moving it would rewrite a broadcast on "+
				"somebody else's channel. Reconnect the account this show was scheduled "+
				"against, or schedule it again on the connected one",
				broadcastID, cid, acct.Ref, acct.Name)
		}
	}
	start := at.UTC().Format(time.RFC3339)
	// A throwaway MetadataResult: writeBroadcastParts reports which fields it
	// applied for the composer's benefit, and a reschedule has no composer to
	// report to. Passing one rather than teaching that function to accept nil
	// keeps the nil check out of the path the composer uses.
	return y.writeBroadcastParts(ctx, accessToken, b,
		BroadcastSettings{ScheduledStart: &start}, &MetadataResult{})
}

// broadcastByID reads one broadcast, with every part a write has to carry back.
func (y *YouTube) broadcastByID(ctx context.Context, accessToken, broadcastID string) (*ytBroadcast, error) {
	var list struct {
		Items []ytBroadcast `json:"items"`
	}
	q := url.Values{}
	// The four parts writeBroadcastParts reads from: snippet for the title,
	// description and current start time, contentDetails for the toggles it must
	// carry through unchanged, status for the lifecycle its refusal advice names.
	q.Set("part", "id,snippet,status,contentDetails")
	q.Set("id", broadcastID)
	err := getJSON(ctx, y.apiEndpoint()+ytBroadcastsPath+"?"+q.Encode(), accessToken, nil, &list)
	if err != nil {
		return nil, err
	}
	// len first, never items[0] blind -- the same rule Stats runs on, and for the
	// same documented reason: nothing on the reference states what a list with no
	// matches returns, so an empty items array is the structurally natural
	// reading rather than a promised one.
	for i := range list.Items {
		if list.Items[i].ID == broadcastID {
			return &list.Items[i], nil
		}
	}
	// Not an error to swallow. A broadcast id polyemesis stored and YouTube no
	// longer lists is an event page that has been deleted from under the
	// schedule, and the caller's retry-and-forget path is the right one for it.
	return nil, fmt.Errorf("YouTube no longer lists broadcast %s on this channel; it may have "+
		"been deleted in YouTube Studio, or the connected account may have changed", broadcastID)
}

// ytBroadcastCreateAdvice turns the create's documented refusals into something
// an operator can act on.
//
// NOT ONE OF THESE MESSAGES CONTAINS A NUMBER, and the concurrency case is why
// this function exists at all. YouTube documents THAT a channel has a maximum
// number of concurrent broadcasts and never documents WHAT it is --
// rateLimitExceeded/concurrentBroadcastsExceedLimit, verbatim: "The channel
// already has the maximum number of concurrent live broadcasts. One or more
// broadcasts that are already live must be stopped before another broadcast can
// start on the channel." Telling an operator "YouTube allows N" would be this
// repository's most-repeated defect, and it would be wrong in the direction that
// makes them stop trying.
//
// The refusal is documented at TRANSITION rather than at create, so reaching it
// here would be a surprise; it is handled anyway because a surprise that reads
// as a generic 403 is how an operator ends up debugging their own credentials.
func ytBroadcastCreateAdvice(err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "liveStreamingNotEnabled"):
		return fmt.Errorf("%w — this YouTube channel is not enabled for live streaming, so no "+
			"broadcast can be created on it at all. Enable live streaming on the channel "+
			"(YouTube verifies the account first, and that can take a day) and try again", err)
	case strings.Contains(msg, "invalidScheduledStartTime"):
		return fmt.Errorf("%w — YouTube refused the start time this show was scheduled for. "+
			"polyemesis sends it as RFC 3339 in UTC and enforces no limit of its own, because "+
			"YouTube publishes none to enforce", err)
	case strings.Contains(msg, "concurrentBroadcastsExceedLimit"),
		strings.Contains(msg, "sharedIngestionBroadcastsExceedLimit"):
		return fmt.Errorf("%w — this channel already has YouTube's maximum number of concurrent "+
			"broadcasts. YouTube does not publish what that number is; end or delete a broadcast "+
			"on the channel and schedule this show again", err)
	}
	return err
}

// deleteBroadcast removes a broadcast this package created and could not
// finish setting up.
//
// ONLY EVER CALLED ON A BROADCAST CREATED SECONDS EARLIER IN THE SAME
// FUNCTION, and that constraint is what makes it safe to have at all. A
// general "delete a YouTube broadcast" helper on this provider would be a
// loaded gun: nothing else in polyemesis should be able to remove an
// operator's event page, and there is no caller that should want to.
//
// A 404 is success. The broadcast not being there is the state this is trying
// to reach, and treating "already gone" as a failure would turn a clean
// outcome into the orphan warning.
func (y *YouTube) deleteBroadcast(ctx context.Context, accessToken, broadcastID string) error {
	if strings.TrimSpace(broadcastID) == "" {
		return fmt.Errorf("no broadcast id to delete")
	}
	q := url.Values{}
	q.Set("id", broadcastID)
	err := requestJSON(ctx, http.MethodDelete,
		y.apiEndpoint()+ytBroadcastsPath+"?"+q.Encode(), accessToken, nil, nil, nil)
	if err == nil {
		return nil
	}
	var se *statusError
	if errors.As(err, &se) && se.Status == http.StatusNotFound {
		return nil
	}
	return err
}
