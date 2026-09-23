/* The session ending while the console is open.
 *
 * Only App's load-time gate used to read a 401. Every request after that
 * raised a toast saying "not signed in" and left the operator on a console
 * where every button failed the same way, for as long as they kept clicking --
 * and sessions last seven days, so on a console left open this is a weekly
 * event rather than an edge case.
 *
 * IN THE TRANSPORT, NOT AT THE CALL SITES: both HTTP clients (lib/api.ts and
 * lib/autoApi.ts) report every response status here, and this decides. A
 * page added tomorrow is covered without being told, the same argument
 * reportReconcileFailure makes.
 *
 * The one judgement is which 401s do NOT mean the session is gone. The server
 * answers 401 on two routes for a credential the operator just TYPED: a wrong
 * password at sign-in, and a wrong current password on a change. Neither says
 * anything about the session, and ejecting an operator to the login screen for
 * a typo in the change-password form would throw the form away. Named by path
 * rather than by the error sentence, because the sentence is English and is
 * due to be translated. */

const EVENT = "polyemesis:session-ended";

/** Routes whose 401 is about the credential in the request body, not the
 *  session. Paths as the clients write them, relative to /api/v1. */
const TYPED_CREDENTIAL_ROUTES: ReadonlySet<string> = new Set(["/auth/login", "/auth/password"]);

/** Called by both HTTP clients with every response they receive. */
export function noteResponseStatus(path: string, status: number): void {
  if (status !== 401) return;
  const route = path.split("?")[0];
  if (TYPED_CREDENTIAL_ROUTES.has(route)) return;
  if (typeof window === "undefined") return;
  window.dispatchEvent(new Event(EVENT));
}

/** Subscribe to the session ending. Returns the unsubscribe. */
export function onSessionEnded(listener: () => void): () => void {
  window.addEventListener(EVENT, listener);
  return () => window.removeEventListener(EVENT, listener);
}
