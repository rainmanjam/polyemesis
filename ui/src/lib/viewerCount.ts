import type { AccountStats } from "./types";

/* ===========================================================================
   The one branch that decides whether a live stream reads as an audience of
   nobody.

   GET /api/v1/platforms/accounts/{id}/stats answers 200 in four shapes and
   three of them are easy to collapse into "0 viewers". This module takes the
   branch ONCE, as data, so that:

     - it is testable without a DOM, like upload-verdict.ts and
       rendition-consequence.ts next to it; and
     - no component can accidentally take it differently. A second call site
       writing `stats.viewerCount ?? 0` is exactly the bug internal/oauth's
       *int spent forty lines of comment preventing, and it would be invisible
       in review because it looks like a sensible default.

   `viewerCount` ABSENT AND `viewerCount` ZERO ARE DIFFERENT ANSWERS. Read the
   comment on oauth.LiveStats.ViewerCount for why the wire can say both: YouTube
   omits the key when nobody is watching, when the OWNER HAS HIDDEN THE COUNT,
   and once the broadcast ends — three states, one absent key — and Kick
   documents 0 as the streamer's opt-out. The hidden-count case is the one that
   turns this from pedantry into a bug report: a streamer with an audience gets
   shown zero, on a live stream, with nothing saying the number was never sent.
   docs/DESIGN-SYSTEM.md's rule for the UI is the same one — a false zero on a
   live stream is worse than a blank.
   =========================================================================== */

/** What the panel should say. Deliberately five cases rather than a number and
 *  a nullable, because every one of them renders differently and the compiler
 *  should refuse a renderer that forgets one. */
export type ViewerReadout =
  /** polyemesis cannot ask this platform at all. Not an error, not a zero, and
   *  not a number that is about to arrive: the reason says so in words. */
  | { kind: "unsupported"; reason: string }
  /** The channel is not live. A normal answer. */
  | { kind: "offline" }
  /** The platform gave a number. INCLUDING ZERO — a real, reported 0 is a fact
   *  about a live stream and renders as 0. */
  | { kind: "count"; count: number }
  /** The channel IS LIVE and the platform did not say how many people are
   *  watching. This is the case the whole feature exists for. It must never
   *  render as 0, as "—", or as a spinner that never resolves. */
  | { kind: "notReported" }
  /** The request itself failed. Distinct from every case above, because "we
   *  could not ask just now" is a transient fault the next poll may fix, while
   *  the four above are answers. */
  | { kind: "unreadable" };

/** The single branch. */
export function viewerReadout(res: AccountStats): ViewerReadout {
  if (!res.supported) {
    return { kind: "unsupported", reason: res.reason };
  }
  const stats = res.stats;
  // Defensive rather than decorative: `supported:true` with no stats object is
  // not a shape the handler produces, but reading `.live` off undefined would
  // take the whole settings page down over a viewer count.
  if (!stats) return { kind: "unreadable" };
  if (!stats.live) return { kind: "offline" };

  // `typeof === "number"` rather than `!= null`, `?? 0` or a truthiness test.
  //
  //   stats.viewerCount ?? 0          -> absent becomes an audience of nobody
  //   stats.viewerCount || fallback   -> a REAL zero becomes the fallback
  //   stats.viewerCount != null       -> correct today, but silently admits a
  //                                      JSON `null` or a string as a count
  //
  // Only presence of an actual finite number is a count. Everything else is a
  // platform that did not answer.
  if (typeof stats.viewerCount === "number" && Number.isFinite(stats.viewerCount)) {
    return { kind: "count", count: stats.viewerCount };
  }
  return { kind: "notReported" };
}

/** How often the panel asks, in milliseconds, and why it is not faster.
 *
 *  60 SECONDS, AND THE NUMBER IS A QUOTA DECISION RATHER THAN A TASTE ONE.
 *
 *  YouTube is the constraint. One Stats call is three requests — channels.list
 *  to learn whose channel this is, liveBroadcasts.list to find the active
 *  broadcast, videos.list to read the count — against a PROJECT-WIDE ceiling of
 *  10,000 units per day that metadata push, compliance and chat all draw from
 *  as well. internal/oauth/youtube.go:280 refuses to state a total because
 *  liveBroadcasts.list does not document its cost; videos.list documents 1 unit
 *  and channels.list documents 1, so two units per poll is a FLOOR, not a
 *  measurement.
 *
 *  At that floor, one connected YouTube account polled every 60s costs 120
 *  units an hour. A four-hour broadcast with this tab open is ~480 units, under
 *  5% of the day. Halving the interval doubles both numbers, and the thing that
 *  breaks first is not this panel: the quota is shared, so the refusal lands on
 *  whichever feature asks next, and an operator sees their TITLE fail to push
 *  because a settings tab was counting viewers. That is why this is slower than
 *  it could be rather than as fast as it can be.
 *
 *  Kick pushes the other way and lands in the same place: it publishes no rate
 *  limit at all, and an undocumented limit is a reason to be conservative, not
 *  a licence — the same reading that put Rumble's chat poll at ten seconds.
 *
 *  Twitch's Get Streams is the only cheap one here, and it does not get its own
 *  interval: one number per panel is worth less than a second call site of this
 *  arithmetic that can drift from it.
 *
 *  This is a POLL and not an event type, per PlayoutPage.tsx:101 and
 *  MonitoringPage.tsx:434. A new event type is expensive — internal/events
 *  AllTypes() is AST-guarded, and a missing entry is a Type nobody was forced
 *  to classify, sent unredacted to whatever principal is listening. A viewer
 *  count on a settings tab does not justify that. */
export const VIEWER_POLL_MS = 60_000;
