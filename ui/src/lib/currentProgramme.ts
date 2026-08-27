/* Which programme the console is looking at.
 *
 * The server refuses a programme-shaped route with `source_required` when an
 * install has two or more sources and the request names none, because answering
 * the DEFAULT is what let five separate bugs put programme 1's figures on
 * somebody else's screen. The client therefore has to have an answer, and until
 * now it had none: `sourceQuery` existed in api.ts and not one caller supplied
 * an id.
 *
 * Deliberately NOT a "selected source" with a picker. There is no picker yet,
 * and inventing one is a design question. What this does is give every request
 * a programme it can NAME, chosen by a rule that is written down, so a wrong
 * answer is attributable instead of anonymous.
 *
 * The rule: whatever the operator last looked at, else the first source the
 * server lists. The server's order is source display order, so "the first one"
 * is the same programme the server would have defaulted to -- this changes what
 * the request SAYS, not which programme it gets, on the installs where the old
 * behaviour was already right. On the installs where it was wrong, it is now
 * wrong out loud. */

const KEY = "polyemesis.currentProgramme";

/** Read the remembered programme, or null if there is none to remember.
 *
 * Storage can throw -- a private window, a browser set to block site data --
 * and a console that will not render because it could not read a preference is
 * a worse failure than forgetting one. */
export function rememberedProgramme(): number | null {
  try {
    const raw = window.localStorage.getItem(KEY);
    if (!raw) return null;
    const n = Number(raw);
    return Number.isSafeInteger(n) && n > 0 ? n : null;
  } catch {
    return null;
  }
}

export function rememberProgramme(id: number | null): void {
  try {
    if (id == null) window.localStorage.removeItem(KEY);
    else window.localStorage.setItem(KEY, String(id));
  } catch {
    /* see rememberedProgramme */
  }
}

/** The programme to ask about, given what the server lists and what was
 *  remembered.
 *
 *  Returns null when the install has NO sources -- the setup wizard's state.
 *  Null is the honest answer there and the routes accept it, because with no
 *  sources there is no ambiguity to refuse.
 *
 *  A remembered id that the server no longer lists is discarded rather than
 *  sent: a deleted programme would otherwise produce a 409 on every poll, and
 *  the operator would see a dead console with no way to guess why. */
export function resolveProgramme(available: readonly number[], remembered: number | null): number | null {
  if (available.length === 0) return null;
  if (remembered != null && available.includes(remembered)) return remembered;
  return available[0];
}
