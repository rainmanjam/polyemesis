import { describe, expect, it } from "vitest";
import { translate } from "./i18n";
import { failedRead, okRead, pendingRead } from "./readState";
import { overviewTiles, type OverviewReads } from "./settingsOverview";
import type { PlatformAccount, PlatformCreds, SetupGuide, Settings } from "./types";

/* THE OVERVIEW NEVER TURNS A FAILED READ INTO AN EMPTY INSTALL (settings-council R5). */

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
const tile = (r: OverviewReads, tab: string, extra: Partial<Parameters<typeof overviewTiles>[0]> = {}) =>
  overviewTiles({ settings, sourceCount: 2, defaultSourceName: "Main", secretKeyBackedUp: true, reads: r, ...extra }, t).find(
    (x) => x.tab === tab,
  )!;

describe("overviewTiles", () => {
  it("says 'could not read' for a platform read that failed, never '0 connected'", () => {
    const platforms = tile(reads({ accounts: failedRead() }), "platforms");
    expect(platforms.lines).toEqual([t("settingsUx.tileCouldNotRead")]);
  });

  it("counts connected platforms among the ones this build can connect, and flags a refused pair", () => {
    const platforms = tile(
      reads({
        guides: okRead([
          { platform: "twitch", supported: true },
          { platform: "youtube", supported: true },
          { platform: "rumble", supported: false },
        ] as SetupGuide[]),
        accounts: okRead([{ platform: "twitch" }] as PlatformAccount[]),
        creds: okRead([{ platform: "youtube", lastCheck: { state: "rejected" } }] as PlatformCreds[]),
      }),
      "platforms",
    );
    expect(platforms.lines[0]).toBe(t("settingsUx.tilePlatforms", { connected: 1, total: 2 }));
    expect(platforms.lines).toContain(t("settingsUx.tileRefused", { n: 1 }));
    expect(platforms.attention).toBe(true);
  });

  it("names the programme the Ingest tab edits", () => {
    expect(tile(reads(), "ingest").lines).toEqual([
      t("settingsUx.tileProgrammes", { n: 2 }),
      t("settingsUx.scopeProgramme", { name: "Main" }),
    ]);
  });

  it("flags an install with no source, and an unknown source count is not zero", () => {
    expect(tile(reads(), "ingest", { sourceCount: 0 }).attention).toBe(true);
    expect(tile(reads(), "ingest", { sourceCount: null }).lines).toEqual([t("settingsUx.tileCouldNotRead")]);
  });

  it("asks for the secret.key backup until it is confirmed", () => {
    expect(tile(reads(), "security", { secretKeyBackedUp: false }).attention).toBe(true);
    expect(tile(reads(), "security", { secretKeyBackedUp: false }).lines).toContain(t("settingsUx.tileNotBackedUp"));
  });

  it("flags a certificate inside its last 30 days", () => {
    const security = tile(
      reads({ tls: okRead({ mode: "acme", certificate: { daysRemaining: 1, expired: false } } as never) }),
      "security",
    );
    expect(security.lines).toContain(t("settingsUx.tileCertExpiring", { left: t("set.certDaysLeftOne", { n: 1 }) }));
    expect(security.attention).toBe(true);
  });

  it("summarises the pipeline from the stored settings", () => {
    expect(tile(reads(), "pipeline").lines).toEqual([
      t("settingsUx.tileFailoverOn"),
      t("settingsUx.tileStagger", { ms: 250 }),
    ]);
  });

  it("reads OBS as switched off, connected, or switched on and not connected", () => {
    const obs = (enabled: boolean, state: string) =>
      tile(reads({ obs: okRead({ config: { enabled }, status: { connection: { state } } } as never) }), "obs");
    expect(obs(false, "off").lines).toEqual([t("settingsUx.tileObsOff")]);
    expect(obs(true, "connected").lines).toEqual([t("settingsUx.tileObsConnected")]);
    expect(obs(true, "refused")).toMatchObject({ lines: [t("settingsUx.tileObsNotConnected")], attention: true });
    expect(tile(reads({ obs: failedRead() }), "obs").lines).toEqual([t("settingsUx.tileCouldNotRead")]);
  });
});
