# Compliance metadata has never been sent

**What:** wire `PushCompliance` to something. YouTube's COPPA self-declaration
and privacy status, and Twitch's content classification labels, are stored,
editable and validated — and no code path has ever sent them to a platform.

**Why:** `db.Compliance`'s own doc comment says it plainly:

> Compliance metadata: the fields that are not a nicety. These describe the
> OBLIGATION: who the programme is for, who may see it, and what a viewer is
> about to be shown. COPPA is a law, Twitch requires labels for several content
> classes, and going live publicly by accident cannot be undone once people have
> seen it.

An operator sets those, sees them saved, and every one of them stops at the
database.

## How it was found, and what that says

Roadmap item 10 Part D is recorded as shipped, "verified against the tree rather
than against the PR titles ... `MetadataCaps` on all four providers plus
`PushCompliance`". That verification confirmed the function **existed**. Nothing
checked that anything **called** it.

Found while writing docs for a different sub-project, by following an instruction
to describe the code rather than the summary. Confirmed four ways: no interface
declares `PushCompliance`, no non-test caller exists, `youtube_broadcast.go`
mentions `privacyStatus` only in a comment warning, and the only consumer of
`dest.Compliance` outside `internal/db` is a line added by 10D.

`internal/oauth/ui_drift_test.go` carries the comment *"The compliance fields are
pushed through PushCompliance"* — a comment asserting what the code does not do,
inside a guard file.

## The finding that shaped the design

**Compliance is stored per DESTINATION. The push is per ACCOUNT.**

`metadataTargets` iterates `ListPlatformAccounts`. `Compliance` lives on
`db.Destination`. And a compliance write targets whatever the token owns —
`YouTube.PushCompliance` takes no account reference at all, because the Live
Streaming API scopes every call to the authenticated channel.

So two destinations pointing at one YouTube account with different compliance are
asking one broadcast to be two things at once.

**Decision: push per destination, and refuse a conflict rather than resolve it.**
Group destinations by account; if two carry different compliance for the same
account, the push is refused and both destinations are named. Silently letting
one win would discard a COPPA declaration with nothing anywhere saying so, which
is the failure this whole piece of work exists to end.

## Scope

**In:** a `CompliancePusher` capability discovered the way `MetadataFor` already
is; the existing push endpoint calling it per destination; conflict refusal;
end-to-end guards that the stored values reach the wire; Facebook's privacy
joining the same path.

**Out:**

- **Moving compliance onto the account.** It would remove the conflict by
  construction and is arguably where the data belongs, but it is a schema
  change, a migration with conflicts to resolve, and a UI move — for a
  correctness problem that refusal already handles.
- **New compliance fields.** Nothing here adds a field. Every value already
  exists, is already editable, and is already validated.
- **A separate "push compliance" button.** It rides the existing push, because
  an operator pressing push before going live means "make the platform match
  what I set", and splitting that into two buttons invites doing one and not the
  other.

## The capability

Three providers need three different inputs, so the target is a struct rather
than a widening parameter list — the same reason `IngestOptions` is one:

```go
// ComplianceTarget is what a compliance write needs BESIDES the token, which
// differs per platform and is why this is a struct.
type ComplianceTarget struct {
    // AccountRef is the channel id recorded when the account was connected.
    // Twitch needs it; YouTube ignores it, because the Live Streaming API
    // scopes every call to the authenticated channel.
    AccountRef string
    // StreamKey is the DESTINATION's, and Facebook alone needs it: its live
    // video id is recoverable from the stored key (FacebookLiveVideoID), and
    // privacy is a property of that broadcast rather than of the account.
    StreamKey string
}

type CompliancePusher interface {
    Provider
    PushCompliance(ctx context.Context, clientID, accessToken string,
        tgt ComplianceTarget, c db.Compliance) (*MetadataResult, error)
}

// ComplianceFor returns the capability, or false when a platform has none.
// Discover it; never type-assert at a call site, because "absent" is a
// supported answer and has to be handled once.
func ComplianceFor(p db.Platform) (CompliancePusher, bool)
```

YouTube and Twitch keep their existing bodies behind the new signature. Facebook
gains one that calls `UpdateLiveVideoPrivacy` — the method 10D built, tested, and
left with no caller precisely because this dispatch did not exist.

## What each platform actually does

Recorded because the asymmetry is the trap, and `compliance.go` already warns
about it:

| Platform | Fields | Note |
|---|---|---|
| YouTube | `privacyStatus`, `selfDeclaredMadeForKids` | **Two endpoints.** Privacy is `liveBroadcasts.update part=status`; made-for-kids is absent from update's settable list and has to go through `videos.update`. Its own comment: *"Anyone who assumes symmetry here writes a call that returns 200 and changes nothing."* |
| Twitch | `labels` | Read shape and write shape differ. `MatureGame` is readable and not writable, and is deliberately absent from the offered set. |
| Facebook | `facebookPrivacy` | Applied at broadcast create; this path changes it afterwards, and reports success only when a read-back confirms it. Suppressed for Page targets. |

## Failure behaviour

**Per field, as `MetadataResult` already does.** Partial success is normal: a
privacy that lands and a made-for-kids that does not is a real state, and the
existing composer already renders `Applied` and `Skipped`.

**A conflict refuses the whole push for that account**, naming both
destinations, before anything is sent. Refusing after a partial write would
leave the operator worse off than not trying.

**A platform with no compliance capability is absent, not failing.** Kick has no
compliance surface; it must not appear as an error.

**An empty `Compliance` sends nothing.** `Compliance.Empty()` already exists and
is already correct; a destination that has never been given a compliance setting
must produce exactly the API calls it produced before this existed.

## Testing

Every guard proven able to fail against a named one-line mutation.

| Case | Why it matters |
|---|---|
| A stored YouTube privacy reaches `liveBroadcasts.update` | The whole point; asserted on the request, not on a call being made |
| A stored made-for-kids reaches `videos.update`, not the broadcast endpoint | The documented trap: the symmetric call returns 200 and changes nothing |
| Stored Twitch labels reach the channel in the WRITE shape | Read shape sent as a write is rejected, and the operator sees a go-live fail for no visible reason |
| Facebook privacy goes through the confirmed path | Reporting applied without a read-back is what 10D established it must never do |
| Two destinations, one account, different compliance → refused, both named | The decision this design turns on |
| Two destinations, one account, IDENTICAL compliance → pushed once | The conflict rule must not refuse agreement |
| A destination with empty compliance produces no compliance request at all | Same "empty means leave alone" invariant the rest of this area runs on |
| A platform with no capability is absent from the results, not failed | Kick |
| **The dispatch is called at all** | The defect being fixed is that nothing called it. A test of `ComplianceFor` that never proves the endpoint invokes it would repeat the bug one level in — which happened twice in 10D. |

The last row is the one to write first.

## What could go wrong

**This sends things that have never been sent.** Every install with stored
compliance starts writing it to platforms on the next push. That is the point,
and it is still a behaviour change an operator did not ask for today: a
destination configured months ago with a privacy setting nobody remembers will
apply it. The release note has to say so plainly.

**The conflict rule can block a push that used to work.** Two destinations on one
account are legal today and, with different compliance, will now refuse. That is
the correct outcome and it will look like a regression to whoever hits it, so the
message must name both destinations and say what to change.

**Facebook's privacy path is the least certain part.** Graph documents no update
surface for `LiveVideo`, which is why 10D confirms by read-back. If the read-back
proves unreliable in the field, the honest fallback is to report it as skipped
rather than to loosen the check.
