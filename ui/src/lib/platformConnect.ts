import type { PlatformCreds, SetupGuide } from "./types";

/* ===========================================================================
   Which ways a platform card offers to connect an account.

   THE DECISION, NOT THE BUTTONS. SettingsPage renders three controls in one
   row and every one of them used to be gated by an expression written inline
   in the JSX -- which is a branch that can only be exercised by rendering a
   2,800-line page, i.e. one that in practice is never exercised at all. The
   same reasoning that put deviceFlow.ts beside DeviceCodeDialog.tsx and
   scheduleActions.ts beside AutomationPage.tsx applies here: the rule is a
   value, so a test can hold it.

   TWO RULES LIVE HERE AND BOTH HAVE A FAILURE ATTACHED.

   The code route is offered BESIDE the redirect route, never instead of it.
   They are for different situations and only the operator knows which they are
   in: the redirect button needs a URI the platform will accept, which a box
   reached as https://192.168.1.50 or on a self-signed certificate does not
   have -- while an operator on a normal hostname wants the one-click one.
   Hiding either would be this module guessing at the deployment.

   Nothing is offered before the credentials are stored. Both routes start with
   the operator's own client ID, so a connect button on a card with no
   credentials starts a flow that can only fail, and fails at the platform --
   where the sentence explaining why is not ours to write.
   =========================================================================== */

/** One control in the platform card's action row, in the order they appear. */
export type ConnectControl =
  /** The ordinary OAuth handoff: the platform redirects the browser back. */
  | "connectRedirect"
  /** The device code flow: a code typed on a phone, no callback needed. */
  | "connectWithCode"
  /** Forget the stored client ID and secret. */
  | "removeCredentials";

/** Whether this build may offer the device code flow for this platform.
 *
 *  READ OFF THE GUIDE, NEVER MATCHED AGAINST A PLATFORM NAME. The server
 *  derives the field from oauth.DeviceFor, so a build that gains or loses the
 *  capability changes what this UI offers with no edit here -- which is the
 *  whole reason it is a field and not a constant. A `platform === "twitch"`
 *  written in the page would be a second, private copy of that registry, and
 *  the day it disagreed the symptom would be a button that starts a flow the
 *  server has no provider for. */
export function platformSupportsDeviceCode(
  guide: Pick<SetupGuide, "deviceFlow">,
): boolean {
  return guide.deviceFlow === true;
}

/** The controls the card should show, given the platform and what is stored. */
export function platformConnectControls(
  guide: Pick<SetupGuide, "deviceFlow">,
  creds: PlatformCreds | null | undefined,
): ConnectControl[] {
  if (!creds) return [];
  const controls: ConnectControl[] = ["connectRedirect"];
  if (platformSupportsDeviceCode(guide)) controls.push("connectWithCode");
  controls.push("removeCredentials");
  return controls;
}
