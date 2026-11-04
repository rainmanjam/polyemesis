import { describe, expect, it } from "vitest";
import { translate } from "./i18n";
import { pendingRead, okRead } from "./readState";
import { overviewTiles, type OverviewReads } from "./settingsOverview";
import type { PlatformAccount, PlatformCreds, SetupGuide, Settings } from "./types";

/* The Overview's Platforms tile reads one as one. The catalogue has no plural
 * rules, so an install whose build can connect a single platform has its own
 * key: it said "Connected: 0 of 1 platforms". The form follows the total, the
 * number the noun belongs to. */

const t = ((key: Parameters<typeof translate>[1], vars?: Record<string, string | number>) =>
  translate("en", key, vars)) as Parameters<typeof overviewTiles>[1];

function reads(over: Partial<OverviewReads> = {}): OverviewReads {
  return {
    destinations: okRead(0),
    creds: okRead<PlatformCreds[]>([]),
    accounts: okRead<PlatformAccount[]>([]),
    guides: okRead<SetupGuide[]>([]),
    overlays: okRead(0),
    obs: pendingRead(),
    tls: pendingRead(),
    tokens: okRead(0),
    ...over,
  };
}

const settings = { failover: { enabled: true }, destinations: { staggerMs: 250 } } as Settings;
const tile = (r: OverviewReads, tab: string) =>
  overviewTiles({ settings, sourceCount: 2, defaultSourceName: "Main", secretKeyBackedUp: true, reads: r }, t).find(
    (x) => x.tab === tab,
  )!;

describe("overviewTiles: the Platforms count", () => {
  it("says Connected: 0 of 1 platform when the build can connect one", () => {
    const platforms = tile(
      reads({
        guides: okRead([
          { platform: "twitch", supported: true },
          { platform: "rumble", supported: false },
        ] as SetupGuide[]),
      }),
      "platforms",
    );
    expect(platforms.lines[0]).toBe("Connected: 0 of 1 platform");
    expect(platforms.lines[0]).not.toBe("Connected: 0 of 1 platforms");
  });

  it("says Connected: 1 of 2 platforms when the build can connect two", () => {
    const platforms = tile(
      reads({
        guides: okRead([
          { platform: "twitch", supported: true },
          { platform: "youtube", supported: true },
        ] as SetupGuide[]),
        accounts: okRead([{ platform: "twitch" }] as PlatformAccount[]),
      }),
      "platforms",
    );
    expect(platforms.lines[0]).toBe("Connected: 1 of 2 platforms");
  });
});
