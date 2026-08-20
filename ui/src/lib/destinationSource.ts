import type { SourceView } from "@/lib/types";

/** A programme a destination can be attached to. */
export interface SourceChoice {
  id: number;
  name: string;
}

/**
 * Which programmes a destination may be attached to.
 *
 * A name is what the operator picks by, so a source with none gets a stable
 * stand-in rather than an empty row that cannot be told from its neighbour.
 */
export function sourceChoices(sources: readonly SourceView[]): SourceChoice[] {
  return sources.map((s) => ({
    id: s.id,
    name: s.name?.trim() ? s.name : `Source ${s.id}`,
  }));
}

/**
 * What the source picker starts on.
 *
 * THE EMPTY ANSWER IS THE POINT, and it is the whole reason this is a function
 * rather than a default value written into the component.
 *
 * Editing: the destination's own programme, always. Anything else would silently
 * offer to move it.
 *
 * Creating, with one programme: that one. There is no choice to make, so
 * demanding a click is ceremony -- the operator would be picking the only item
 * in the list.
 *
 * Creating, with several: NOTHING. This is the case the whole change exists for.
 * The server used to pick the first source when a body named none, so a
 * destination could be created against a programme nobody chose and nothing on
 * screen said which. Preselecting the first here would reproduce that bug in the
 * UI exactly -- a default that looks like a decision -- so the picker starts
 * empty and the save stays disabled until someone chooses.
 */
export function initialSourceValue(
  destination: { sourceId?: number | null } | null,
  sources: readonly SourceChoice[],
): string {
  if (destination?.sourceId) return String(destination.sourceId);
  if (sources.length === 1) return String(sources[0].id);
  return "";
}

/**
 * Whether the form may be saved, as far as the source is concerned.
 *
 * Checked against the LIST, not merely for non-emptiness: a stale id -- a
 * programme deleted in another tab while this dialog sat open -- would otherwise
 * pass here and be refused by the server's foreign key, which reports it as a
 * failed save rather than as a choice that is no longer available.
 */
export function sourceIsChosen(value: string, sources: readonly SourceChoice[]): boolean {
  const id = Number(value);
  return Number.isFinite(id) && id > 0 && sources.some((s) => s.id === id);
}

/**
 * The sourceId a save should send, or null when there is nothing valid to send.
 *
 * Null is never a payload here. The server refuses a create that names no
 * source, so a caller that reads null must not send -- which is what
 * sourceIsChosen gates, and this returning null is the second line of that.
 */
export function sourceIdForSave(value: string, sources: readonly SourceChoice[]): number | null {
  return sourceIsChosen(value, sources) ? Number(value) : null;
}
