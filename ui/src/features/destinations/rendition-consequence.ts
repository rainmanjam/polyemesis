/** What choosing — or abandoning — a shared video encode actually costs.
 *
 *  A rendition is shared and ref-counted: the first enabled destination on it
 *  starts one encode, every later one joins that same encode for free, and it
 *  stops when the last enabled destination leaves. That arithmetic is the whole
 *  reason renditions exist, and it is invisible on a dropdown that shows only a
 *  name. `docs/evidence/video-treatment-ui.md` has the full argument.
 *
 *  These live outside the dialog because they are the part worth testing. In
 *  JSX they were four nested ternaries inside a template literal — reachable
 *  only by rendering the component with the right server fixtures, so in
 *  practice never checked at all. Off-by-one in `lastOut` is the expensive
 *  mistake here: it tells an operator their colleague's encode is about to stop
 *  when it is not, or stays silent when it is.
 */

/** The usage counts the API already sends with every rendition. */
export interface RenditionUsage {
  /** Destinations configured to use this encode, enabled or not. */
  destinations: number;
  /** Of those, the ones actually enabled — the count that decides whether an
   *  encode is running, because a disabled destination costs nothing. */
  enabledDestinations: number;
}

/** What happens when this destination JOINS the selected encode.
 *
 *  The distinction is free-vs-not: joining something already running adds no
 *  process, while being the first one on an idle encode starts one. */
export function joinConsequence(usage: RenditionUsage): string {
  if (usage.enabledDestinations > 0) {
    const n = usage.destinations;
    return `Feeds ${n} destination${n === 1 ? "" : "s"} · already encoding. This destination joins the running encode — no new encode starts.`;
  }
  return "Starts one shared encode when an enabled destination uses it.";
}

/** The encode being left behind, and whether this departure stops it. */
export interface Leaving {
  name: string;
  /** True when removing this destination takes the encode's enabled count to
   *  zero, so the encode stops. */
  lastOut: boolean;
  /** Enabled destinations that remain on it afterwards. */
  others: number;
}

/** Work out what the abandoned encode does once this destination has gone.
 *
 *  Returns null when there is nothing to say: no previous encode, or the
 *  selection has not actually moved.
 *
 *  @param previousRenditionId the encode this destination is saved with
 *  @param selectedRenditionId the encode now chosen, as the form's string value
 *         ("" for copy/passthrough)
 *  @param enabled whether this destination is itself enabled — a disabled one
 *         was never counted, so leaving cannot stop anything
 *  @param usageOf usage counts for a rendition id, or null if unknown
 */
export function computeLeaving(
  previousRenditionId: number | null | undefined,
  selectedRenditionId: string,
  enabled: boolean,
  usageOf: (id: number) => (RenditionUsage & { name: string }) | null,
): Leaving | null {
  if (!previousRenditionId) return null;
  if (String(previousRenditionId) === selectedRenditionId) return null;

  const was = usageOf(previousRenditionId);
  if (!was) return null;

  // THE OFF-BY-ONE: this destination is still counted in enabledDestinations
  // while the dialog is open, because nothing has been saved yet. So "last one
  // out" is a count of one, not zero — the one being this destination itself.
  //
  // A disabled destination was never in that count, so it can never be the one
  // whose departure stops the encode, however low the count is.
  const lastOut = enabled && was.enabledDestinations <= 1;

  return {
    name: was.name,
    lastOut,
    others: Math.max(0, was.enabledDestinations - 1),
  };
}

/** How to say what `computeLeaving` worked out. */
export function leaveConsequence(leaving: Leaving): string {
  if (leaving.lastOut) {
    return `Stops the “${leaving.name}” encode — no other enabled destination is on it.`;
  }
  const n = leaving.others;
  return `${n} other enabled destination${n === 1 ? "" : "s"} stay on “${leaving.name}”. Nothing else changes.`;
}
