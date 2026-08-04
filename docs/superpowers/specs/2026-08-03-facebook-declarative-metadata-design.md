# Facebook's declarative metadata

**What:** the Facebook fields that are *statements about a broadcast* —
`privacy`, `content_tags`, `crossposting_actions`, `donate_button_charity_id` —
sent from the destination and the composer rather than left unset.

**Why:** polyemesis sends Facebook two fields, `title` and `description`,
against the largest documented write surface of any platform it supports. One of
the unsent fields is not a nicety: Facebook documents
`LIVE_VIDEO__PRIVACY_REQUIRED` — *"You need to set a privacy before going
live."*

This is sub-project **D** of roadmap item 10. E (scheduling) and F (the
ingest-shaped fields) are separate and specified elsewhere.

## The verification that shaped this

Facebook's surface was re-checked against the **v26.0** reference on 2026-08-03
before any design work, because the roadmap entry describing it was written on
2026-07-28 and v26.0 shipped on 2026-07-29.

**The field inventory survived.** `content_tags`, `enable_backup_ingest`,
`stop_on_delete_stream`, `crossposting_actions`, `donate_button_charity_id`,
`privacy`, `event_params` and the 360 fields are all still documented on the
`live_videos` create edge, and `overlay_url` is still removed for v24.0+.

**The framing did not.** Two of the things the roadmap listed as metadata are
not metadata, and separating them is what makes this sub-project small enough to
be worth doing on its own:

- **`inband_go_live`** is a nested modifier on a GET whose side effect hands back
  a *different* ingest URL, after which the broadcast stays invisible until the
  encoder injects an AMF0 `onGoLive` packet naming the first public frame. That
  is FFmpeg and muxer work. → sub-project **F**.
- **`enable_backup_ingest`** returns a *second* ingest URL that something has to
  actually stream to. Setting the flag alone is inert — it would ship as a
  feature that does nothing observable. → sub-project **F**.

**`event_params` scheduling is bounded to seven days** from creation, which
collides with the weekly schedules item 5C shipped in 883ce79. That needs its own
decision and its own spec. → sub-project **E**.

**The version pin is fine.** `internal/oauth/facebook.go` pins v24.0, supported
until 2028-02-18. Neither v25.0 nor v26.0 changed anything in live video.

## What already exists

`internal/oauth/metadata.go` defines the shape this follows:

- `Metadata` — what the operator typed once in the composer: `Title`,
  `Description`, `Category`. **An empty field means "leave whatever the platform
  already has"**, because blanking a live title by accident is worse than
  requiring a second edit.
- `MetadataCaps` — what one platform will accept, so the composer can say
  "Twitch has no description" up front instead of reporting it as a failure
  afterwards. Its `Scope` field is *reported, never enforced*: the platform's own
  401 is the authority, because a capability check wrong in the restrictive
  direction refuses work that would have succeeded.
- `MetadataResult` — per-field `Applied`/`Skipped` plus `Warnings`, because
  partial success is the normal case.
- `db.Compliance` — the obligation fields (`Privacy`, `MadeForKids`, `Labels`),
  stored **per destination as one JSON blob** on `destinations.compliance`.

Facebook today declares `Fields: {title, description}` and implements no
compliance at all. `IngestFor` sends exactly one parameter: `status=LIVE_NOW`.

## The two facts that decided the design

**1. Only the create surface is documented.** The Graph API reference has no
`POST /{live-video-id}` *Updating* row at all — `VideoPoll` gets one, `LiveVideo`
does not — yet `writeLiveVideo` POSTs title and description to `/<id>` and that
ships and works today. So updating a live video works, and *which parameters it
accepts* is written down nowhere. Every field in this spec is documented at
**create** and unverified at **update**.

**2. `IngestFor` is where the `LiveVideo` is created**, and it runs when the
operator fetches the stream key — not at go-live, and not from the composer. It
receives `(ctx, clientID, accessToken, targetRef)` and has no access to the
destination.

## Scope

**In:** Facebook privacy, `content_tags`, `crossposting_actions`,
`donate_button_charity_id`.

**Out:**

- **Everything ingest-shaped** — `enable_backup_ingest`, `stop_on_delete_stream`,
  and frame-accurate go-live. Sub-project F.
- **Scheduling** — `event_params`. Sub-project E.
- **360, spatial audio and fisheye** — `is_spherical`, `projection`,
  `stereoscopic_mode`, `encoding_settings`, and the fisheye fields. Not deferred
  for size: polyemesis cannot make a flat feed spherical, and nothing in the
  pipeline produces or verifies equirectangular video or ambisonic audio.
  Declaring a flat stream as 360 gives Facebook's viewers a broken experience, so
  the field is worse than absent. It is a rendition-path feature, not a checkbox.

## Where each field lives

The three ID-shaped fields split by **how often they change**, not by when they
are applied:

| Field | Changes | Home | Applied |
|---|---|---|---|
| `title`, `description` | Every broadcast | `oauth.Metadata` | Push (shipped) |
| `content_tags` | Every broadcast | `oauth.Metadata.Tags` | Push |
| privacy | Rarely — a property of the destination | `db.Compliance` | **Create**, and best-effort on push |
| `crossposting_actions` | Rarely — which Pages you share with | `db.Destination.Facebook` | **Create** |
| `donate_button_charity_id` | Rarely — a campaign | `db.Destination.Facebook` | **Create** |

Crossposting and donate are opaque IDs an operator obtains once from Facebook's
console and reuses. Typing them into the composer before every broadcast is the
hostile option; storing them on the destination also puts them on the
**documented** create surface rather than the update endpoint Meta does not
describe.

Privacy is applied at create **and** attempted best-effort on push. The create
path is the guarantee; the push path is so that changing privacy later does not
require deleting and re-fetching the stream key.

## Storage

`db.Compliance` gains one field, in the existing blob — **no migration**:

```go
// FacebookPrivacy is Facebook's audience for a live video. Empty means LEAVE IT
// ALONE, exactly as PrivacyStatus does, and for the same reason.
//
// Deliberately NOT PrivacyStatus. That type is documented as YouTube's
// visibility and its values are public/unlisted/private; Facebook has no
// unlisted and YouTube has no friends, so a shared type would need a lossy
// mapping in the one field where being wrong is unrecoverable. A translation
// layer is somewhere for that wrongness to hide.
type FacebookPrivacy string

const (
    FBPrivacyUnchanged        FacebookPrivacy = ""
    FBPrivacySelf             FacebookPrivacy = "SELF"
    FBPrivacyFriends          FacebookPrivacy = "ALL_FRIENDS"
    FBPrivacyFriendsOfFriends FacebookPrivacy = "FRIENDS_OF_FRIENDS"
    FBPrivacyEveryone         FacebookPrivacy = "EVERYONE"
)

// FacebookPrivacies is every value an operator may pick, least exposure first —
// matching PrivacyStatuses, because the safe choice should be the near one.
var FacebookPrivacies = []FacebookPrivacy{
    FBPrivacySelf, FBPrivacyFriends, FBPrivacyFriendsOfFriends, FBPrivacyEveryone,
}
```

`db.Destination` gains one nullable column carrying a small blob, following the
pattern `destinations.compliance` already established ("it is a map plus two
scalars, edited as a unit, and `'{}'` is touch nothing"):

```go
// FacebookSettings is per-destination Facebook configuration applied when the
// broadcast is CREATED. Empty means send nothing.
type FacebookSettings struct {
    // Crosspost names the Pages this broadcast is shared with.
    Crosspost []CrosspostTarget `json:"crosspost,omitempty"`
    // DonateCharityID adds a donate button for one charity.
    DonateCharityID string `json:"donateCharityId,omitempty"`
}

// CrosspostTarget is one Page and what to do with it.
type CrosspostTarget struct {
    PageID string `json:"pageId"`
    // CreatePost also posts as that Page, rather than only enabling the share.
    // Facebook's two actions differ by exactly this.
    CreatePost bool `json:"createPost,omitempty"`
}
```

It reaches the row as one field, beside `Compliance`:

```go
// On db.Destination:
Facebook FacebookSettings `json:"facebook"`
```

Migration: `ALTER TABLE destinations ADD COLUMN facebook TEXT NOT NULL DEFAULT '{}'`,
in the same `columns` list the compliance column uses, and marshalled and scanned
in the same three places `compliance` already is
(`destinations.go:498`, `:641`, `:696`).

`oauth.Metadata` gains `Tags []string` — words, not IDs. `FieldTags` already
exists in `AllMetadataFields`, so results can name it without touching the
UI-nameability drift guard's expectations.

## The interface change

`IngestFor` gains an options struct rather than a fifth string:

```go
// IngestOptions carries what a platform needs at CREATE time, which is not the
// same set as what the composer pushes afterwards.
//
// A struct rather than more parameters because sub-projects E and F both add
// create-time fields -- scheduling and backup ingest -- and three signature
// changes to the same interface is three chances to miss a call site.
type IngestOptions struct {
    Privacy         db.FacebookPrivacy
    Crosspost       []db.CrosspostTarget
    DonateCharityID string
}

IngestFor(ctx context.Context, clientID, accessToken, targetRef string,
    opts IngestOptions) (*Broadcast, error)
```

Facebook is the only implementer and the interface is declared in
`facebook.go`. Two call sites: `facebook.go:446` (its own `Ingest`, which passes
a zero `IngestOptions`) and `api/oauth_handlers.go:437`, whose caller already
holds `dest` and can map `dest.Compliance.FacebookPrivacy` and `dest.Facebook`
into it.

## Resolving tag names

The operator types words. `content_tags` wants numeric ad-interest IDs, which is
the same problem `Category` already solved:

> Category is a human name — "Gaming", "Just Chatting" — because the numeric id
> every platform actually wants is something only that platform's own console
> can tell you.

So Facebook's `PushMetadata` resolves each word against
`GET /search?type=adinterest&q=<word>`, sends the IDs that matched, and reports
what matched in `MetadataResult`. A word that resolves to nothing is a
**warning naming that word** — never a silent drop, because a tag that vanishes
without comment is indistinguishable from one that worked.

**The risk this carries, stated rather than discovered:** `/search?type=adinterest`
is an ads-surface endpoint and may not be reachable with the `publish_video`
scope. That is unverified — it cannot be checked without a live Facebook account,
and this repo has none in CI. **If the search is refused, tags are reported as
`Skipped` with the platform's own advice and the rest of the push proceeds.** A
403 on a tag lookup must never fail a title change seconds before air.

## Failure behaviour

**Create-time failures fail the key fetch, loudly.** A broadcast created with the
wrong privacy is the unrecoverable case this sub-project exists for; returning a
stream key for it would be worse than returning an error.

**Push-time failures are per field.** `Applied` and `Skipped` already carry this,
and the UI already renders both. Privacy on push is the one field that may be
refused by an endpoint whose accepted parameters are undocumented: it is reported
as `Skipped` with Facebook's own message, and **never as an error**, because the
stored value has already been applied at create and the push is the convenience
rather than the guarantee.

**A Page destination reports privacy as not applicable**, up front through
`MetadataCaps`, rather than as a failure afterwards. Page content is public by
nature; offering a control that cannot do anything is the defect item 0 existed
to fix.

**Empty means leave alone, everywhere.** No field in this spec is sent when the
operator has not set it.

## Testing

Every guard must be shown to fail against a named one-line mutation.

| Case | Why it matters |
|---|---|
| A destination with `FacebookPrivacy` set sends it at create | The whole create-time half, asserted on the request Facebook received |
| A destination with no privacy sends **no** `privacy` parameter at all | "Leave it alone" is load-bearing; sent-empty and unsent are different requests, and conflating them is exactly how YouTube's destructive-by-part bug worked |
| Crossposting targets become the documented JSON shape, with `CreatePost` selecting the right action | The two actions differ by whether a post is created; picking the wrong one posts as a Page without being asked |
| A tag word that matches nothing is reported as a warning naming the word | A silently dropped tag is indistinguishable from one that worked |
| A refused tag search leaves title and description applied | A 403 on an ads endpoint must not fail a title change before air |
| A push carrying privacy attempts it, and reports it as `Applied` or `Skipped` | The best-effort half is a scope item, and a scope item with no test is how the previous sub-project shipped an unguarded fix |
| A Page target reports privacy as unsupported in caps, not as a failed push | The composer must say so before, not after |
| `Metadata.Tags` empty sends no `content_tags` | Same argument as privacy |
| Every new field is nameable by the UI | `TestUITypesCanNameEveryMetadataField` already enforces this class |

The two fixtures that need care, because both are the shape that has failed in
this repo before: a tag word that matches **nothing** — a fixture whose search
always succeeds cannot exercise the warning path — and a privacy value of `""`,
where the assertion must be on the **absence of the parameter**, not on its
value, because a request carrying `privacy=` would satisfy a value check.

## What could go wrong

**Changing privacy needs a new broadcast.** Privacy is applied at create, so an
operator who changes it on a destination whose `LiveVideo` already exists is
editing a setting that has already been used. The best-effort push mitigates it;
whether Facebook honours privacy on update is exactly what the reference does not
document. **The UI must say which broadcast a stored privacy will apply to**, or
the operator will believe they changed something they did not.

**The tag search may be unreachable.** See above. If it is refused in practice,
the honest outcome is to drop `content_tags` from this sub-project rather than
ship a field that silently never applies — and the test that proves the refusal
is survivable is what makes that a small decision rather than a rewrite.

**This adds a second privacy concept to the UI.** An operator with a YouTube and
a Facebook destination now sees two privacy controls with different words. That
is the deliberate cost of refusing a lossy mapping, and the composer should show
each one against its own platform rather than side by side.

**`crosspost_shared_pages` is not used here.** Facebook publishes an edge listing
Pages able to share a broadcast, which would make a picker possible — but it is
an edge **on a LiveVideo**, so it needs a broadcast to already exist and cannot
help an operator configuring a destination beforehand. Page IDs are pasted. If
that proves painful, the fix is a picker fed by the account's own Pages, which is
a different endpoint and its own small feature.
