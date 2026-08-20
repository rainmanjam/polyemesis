import { describe, expect, it } from "vitest";

import {
  platformConnectControls,
  platformSupportsDeviceCode,
} from "./platformConnect";
import type { PlatformCreds, SetupGuide } from "./types";

/* The action row on a platform card, decided as data.
 *
 * These are the rules SettingsPage used to spell out in JSX, where nothing
 * could reach them: a branch written inside a 2,800-line page is a branch
 * exercised only by rendering that page. Both of them have a failure attached
 * and neither is visible in a screenshot of the working case.
 */

const guide = (over: Partial<SetupGuide> = {}): SetupGuide => ({
  platform: "twitch",
  name: "Twitch",
  consoleUrl: "https://dev.twitch.tv/console",
  redirectPath: "/api/v1/platforms/oauth/twitch/callback",
  steps: [],
  scopes: null,
  supported: true,
  ...over,
});

const stored: PlatformCreds = {
  platform: "twitch",
  clientId: "client-abc",
  hasSecret: true,
  updatedAt: "2026-08-19T12:00:00Z",
};

describe("platformConnectControls", () => {
  // The point of the whole device-code feature: the operator on a box with no
  // registrable callback needs a second route, and the operator on a normal
  // hostname still wants the one-click one. Only they know which they are, so
  // offering the code route INSTEAD of the redirect would be this module
  // guessing at the deployment -- and would take the easy route away from
  // everybody who could have used it.
  it("offers the code route beside the redirect route, never instead of it", () => {
    const controls = platformConnectControls(guide({ deviceFlow: true }), stored);
    expect(controls).toContain("connectRedirect");
    expect(controls).toContain("connectWithCode");
  });

  it("leaves the redirect route alone on a platform with no device flow", () => {
    expect(platformConnectControls(guide({ deviceFlow: false }), stored)).toEqual([
      "connectRedirect",
      "removeCredentials",
    ]);
  });

  // Order is asserted because it is the order of a row of buttons, and Remove
  // is the destructive one: landing it between two connect buttons puts a
  // misclick next to the control that forgets the operator's client secret.
  it("keeps the destructive control last in the row", () => {
    expect(platformConnectControls(guide({ deviceFlow: true }), stored)).toEqual([
      "connectRedirect",
      "connectWithCode",
      "removeCredentials",
    ]);
  });

  // Both routes begin with the operator's own client ID. A connect button on a
  // card with nothing stored starts a flow that can only fail, and it fails at
  // the platform -- where the sentence explaining why is not ours to write.
  it.each([
    ["never saved", null],
    ["not loaded yet", undefined],
  ])("offers nothing at all when the credentials were %s", (_name, creds) => {
    expect(platformConnectControls(guide({ deviceFlow: true }), creds)).toEqual([]);
    expect(platformConnectControls(guide({ deviceFlow: false }), creds)).toEqual([]);
  });
});

describe("platformSupportsDeviceCode", () => {
  // The capability is the SERVER'S answer, derived from oauth.DeviceFor. A
  // `platform === "twitch"` written in the page would be a second, private copy
  // of that registry, and the day it disagreed the symptom would be a button
  // that starts a flow the server has no provider for.
  it("follows what the server said about this build, not the platform's name", () => {
    expect(platformSupportsDeviceCode(guide({ platform: "twitch", deviceFlow: false }))).toBe(
      false,
    );
    expect(platformSupportsDeviceCode(guide({ platform: "kick", deviceFlow: true }))).toBe(true);
  });

  it("does not offer the button to a twitch card the server said no about", () => {
    const controls = platformConnectControls(
      guide({ platform: "twitch", deviceFlow: false }),
      stored,
    );
    expect(controls).not.toContain("connectWithCode");
  });

  // An older server, or one that dropped the capability, sends no field at all.
  // Absent is "no": offering a flow nothing can serve fails after the operator
  // has already read a code off the screen.
  it("treats an absent capability as no, not as maybe", () => {
    expect(platformSupportsDeviceCode(guide())).toBe(false);
    expect(platformConnectControls(guide(), stored)).not.toContain("connectWithCode");
  });
});
