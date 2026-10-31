import type { TranslationKey, Translator } from "./i18n";
import { mayClaim, readFailed, type ReadState } from "./readState";
import type { ObsResponse, PlatformAccount, PlatformCreds, SetupGuide, Settings, TlsMode, TlsStatus } from "./types";

/* THE OVERVIEW'S TILES, AS A VALUE (settings-council R5).
 *
 * One tile per settings area, each a short summary and a flag for "needs
 * attention". Derived here rather than in the component so every sentence a
 * tile can say -- including "could not read" -- is a case a test can hold. */

export interface OverviewReads {
  destinations: ReadState<number>;
  creds: ReadState<PlatformCreds[]>;
  accounts: ReadState<PlatformAccount[]>;
  guides: ReadState<SetupGuide[]>;
  overlays: ReadState<number>;
  obs: ReadState<ObsResponse>;
  tls: ReadState<TlsStatus>;
  tokens: ReadState<number>;
}

export interface OverviewTile {
  tab: "ingest" | "platforms" | "overlays" | "obs" | "pipeline" | "security";
  title: TranslationKey;
  /** Already translated. The first line is the headline. */
  lines: string[];
  attention: boolean;
}

const TLS_MODE: Record<TlsMode, TranslationKey> = {
  auto: "set.tlsAuto",
  acme: "set.tlsAcme",
  selfsigned: "set.tlsSelfsigned",
  manual: "set.tlsManual",
  off: "set.tlsOff",
};

/** Whether this browser's operator ticked "I have backed this up" on the
 *  Security tab's secret.key card (settings-council R9). Per browser, and the
 *  card says so: polyemesis cannot see a backup. */
export const SECRET_KEY_ACK = "polyemesis.settings.secretKeyBackedUp";

export function readSecretKeyAck(): boolean {
  try {
    return window.localStorage.getItem(SECRET_KEY_ACK) === "1";
  } catch {
    return false;
  }
}

export function overviewTiles(
  input: {
    settings: Settings;
    sourceCount: number | null;
    defaultSourceName: string | null;
    secretKeyBackedUp: boolean;
    reads: OverviewReads;
  },
  t: Translator,
): OverviewTile[] {
  const { settings, sourceCount, defaultSourceName, secretKeyBackedUp, reads } = input;
  const couldNot = t("settingsUx.tileCouldNotRead");
  const reading = t("settingsUx.reading");
  const pending = (r: ReadState<unknown>) => (readFailed(r) ? couldNot : reading);

  // Ingest: a count the page already has, and the programme the tab edits.
  const ingest: OverviewTile = {
    tab: "ingest",
    title: "set.ingest",
    lines:
      sourceCount === null
        ? [couldNot]
        : sourceCount === 0
          ? [t("settingsUx.tileNoSource")]
          : [
              t("settingsUx.tileProgrammes", { n: sourceCount }),
              ...(defaultSourceName ? [t("settingsUx.scopeProgramme", { name: defaultSourceName })] : []),
            ],
    attention: sourceCount === 0,
  };

  // Platforms: connected out of the platforms this build can connect, and any
  // pair the platform refused at its last check.
  let platforms: OverviewTile;
  const { guides, accounts, creds } = reads;
  if (mayClaim(guides) && mayClaim(accounts) && mayClaim(creds)) {
    const connectable = guides.value.filter((g) => g.supported);
    const connected = connectable.filter((g) => accounts.value.some((a) => a.platform === g.platform));
    const refused = creds.value.filter((c) => c.lastCheck?.state === "rejected").length;
    const reconnect = accounts.value.some((a) => a.reconnect?.needed);
    platforms = {
      tab: "platforms",
      title: "set.tabPlatforms",
      lines: [
        t("settingsUx.tilePlatforms", { connected: connected.length, total: connectable.length }),
        ...(refused > 0 ? [t("settingsUx.tileRefused", { n: refused })] : []),
        ...(reconnect ? [t("settingsUx.pillReconnect")] : []),
      ],
      attention: refused > 0 || reconnect,
    };
  } else {
    const failed = readFailed(guides) || readFailed(accounts) || readFailed(creds);
    platforms = { tab: "platforms", title: "set.tabPlatforms", lines: [failed ? couldNot : reading], attention: false };
  }

  const overlays: OverviewTile = {
    tab: "overlays",
    title: "set.tabOverlays",
    lines: [mayClaim(reads.overlays) ? t("settingsUx.tileOverlays", { n: reads.overlays.value }) : pending(reads.overlays)],
    attention: false,
  };

  let obs: OverviewTile;
  if (mayClaim(reads.obs)) {
    const { config, status } = reads.obs.value;
    const state = status.connection.state;
    const line = !config.enabled
      ? t("settingsUx.tileObsOff")
      : state === "connected"
        ? t("settingsUx.tileObsConnected")
        : t("settingsUx.tileObsNotConnected");
    obs = { tab: "obs", title: "obs.tab", lines: [line], attention: config.enabled && state !== "connected" };
  } else {
    obs = { tab: "obs", title: "obs.tab", lines: [pending(reads.obs)], attention: false };
  }

  const pipeline: OverviewTile = {
    tab: "pipeline",
    title: "set.tabPipeline",
    lines: [
      settings.failover?.enabled ? t("settingsUx.tileFailoverOn") : t("settingsUx.tileFailoverOff"),
      t("settingsUx.tileStagger", { ms: settings.destinations?.staggerMs ?? 0 }),
    ],
    attention: false,
  };

  const securityLines: string[] = [];
  let securityAttention = !secretKeyBackedUp;
  if (mayClaim(reads.tls)) {
    const tls = reads.tls.value;
    securityLines.push(t("settingsUx.tileTls", { mode: t(TLS_MODE[tls.mode]) }));
    const cert = tls.certificate;
    if (cert && cert.daysRemaining < 30) {
      // The transport card's own words for the time left, so the two never
      // disagree and "1 days" never appears.
      const n = Math.abs(cert.daysRemaining);
      const left = cert.expired
        ? n === 0
          ? t("set.certExpiredToday")
          : t(n === 1 ? "set.certExpiredAgoOne" : "set.certExpiredAgoMany", { n })
        : t(n === 1 ? "set.certDaysLeftOne" : "set.certDaysLeftMany", { n });
      securityLines.push(t("settingsUx.tileCertExpiring", { left }));
      securityAttention = true;
    }
  } else {
    securityLines.push(pending(reads.tls));
  }
  securityLines.push(
    mayClaim(reads.tokens) ? t("settingsUx.tileTokens", { n: reads.tokens.value }) : pending(reads.tokens),
  );
  securityLines.push(secretKeyBackedUp ? t("settingsUx.tileBackedUp") : t("settingsUx.tileNotBackedUp"));
  const security: OverviewTile = {
    tab: "security",
    title: "set.tabSecurity",
    lines: securityLines,
    attention: securityAttention,
  };

  return [ingest, platforms, overlays, obs, pipeline, security];
}
