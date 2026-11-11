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

/** The two words every pending tile can show, already translated. */
interface PendingCopy {
  couldNot: string;
  reading: string;
}

/** "Could not read" when the read failed, otherwise "reading". */
const pendingOf = (r: ReadState<unknown>, copy: PendingCopy): string =>
  readFailed(r) ? copy.couldNot : copy.reading;

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
  const copy: PendingCopy = { couldNot: t("settingsUx.tileCouldNotRead"), reading: t("settingsUx.reading") };
  return [
    ingestTile(sourceCount, defaultSourceName, t, copy),
    platformsTile(reads, t, copy),
    overlaysTile(reads, t, copy),
    obsTile(reads, t, copy),
    pipelineTile(settings, t),
    securityTile(reads, secretKeyBackedUp, t, copy),
  ];
}

// Ingest: a count the page already has, and the programme the tab edits.
function ingestTile(
  sourceCount: number | null,
  defaultSourceName: string | null,
  t: Translator,
  copy: PendingCopy,
): OverviewTile {
  let lines: string[];
  if (sourceCount === null) {
    lines = [copy.couldNot];
  } else if (sourceCount === 0) {
    lines = [t("settingsUx.tileNoSource")];
  } else {
    lines = [
      t("settingsUx.tileProgrammes", { n: sourceCount }),
      ...(defaultSourceName ? [t("settingsUx.scopeProgramme", { name: defaultSourceName })] : []),
    ];
  }
  return { tab: "ingest", title: "set.ingest", lines, attention: sourceCount === 0 };
}

// Platforms: connected out of the platforms this build can connect, and any
// pair the platform refused at its last check.
function platformsTile(reads: OverviewReads, t: Translator, copy: PendingCopy): OverviewTile {
  const { guides, accounts, creds } = reads;
  if (!(mayClaim(guides) && mayClaim(accounts) && mayClaim(creds))) {
    const failed = readFailed(guides) || readFailed(accounts) || readFailed(creds);
    return { tab: "platforms", title: "set.tabPlatforms", lines: [failed ? copy.couldNot : copy.reading], attention: false };
  }
  const connectable = guides.value.filter((g) => g.supported);
  const connected = connectable.filter((g) => accounts.value.some((a) => a.platform === g.platform));
  const refused = creds.value.filter((c) => c.lastCheck?.state === "rejected").length;
  const reconnect = accounts.value.some((a) => a.reconnect?.needed);
  return {
    tab: "platforms",
    title: "set.tabPlatforms",
    lines: [
      t(connectable.length === 1 ? "settingsUx.tilePlatformsOne" : "settingsUx.tilePlatforms", { connected: connected.length, total: connectable.length }),
      ...(refused > 0 ? [t("settingsUx.tileRefused", { n: refused })] : []),
      ...(reconnect ? [t("settingsUx.pillReconnect")] : []),
    ],
    attention: refused > 0 || reconnect,
  };
}

function overlaysTile(reads: OverviewReads, t: Translator, copy: PendingCopy): OverviewTile {
  return {
    tab: "overlays",
    title: "set.tabOverlays",
    lines: [mayClaim(reads.overlays) ? t("settingsUx.tileOverlays", { n: reads.overlays.value }) : pendingOf(reads.overlays, copy)],
    attention: false,
  };
}

function obsTile(reads: OverviewReads, t: Translator, copy: PendingCopy): OverviewTile {
  if (!mayClaim(reads.obs)) {
    return { tab: "obs", title: "obs.tab", lines: [pendingOf(reads.obs, copy)], attention: false };
  }
  const { config, status } = reads.obs.value;
  const connected = status.connection.state === "connected";
  return {
    tab: "obs",
    title: "obs.tab",
    lines: [obsLine(config.enabled, connected, t)],
    attention: config.enabled && !connected,
  };
}

function obsLine(enabled: boolean, connected: boolean, t: Translator): string {
  if (!enabled) return t("settingsUx.tileObsOff");
  return t(connected ? "settingsUx.tileObsConnected" : "settingsUx.tileObsNotConnected");
}

function pipelineTile(settings: Settings, t: Translator): OverviewTile {
  return {
    tab: "pipeline",
    title: "set.tabPipeline",
    lines: [
      settings.failover?.enabled ? t("settingsUx.tileFailoverOn") : t("settingsUx.tileFailoverOff"),
      t("settingsUx.tileStagger", { ms: settings.destinations?.staggerMs ?? 0 }),
    ],
    attention: false,
  };
}

function securityTile(reads: OverviewReads, secretKeyBackedUp: boolean, t: Translator, copy: PendingCopy): OverviewTile {
  const securityLines: string[] = [];
  let securityAttention = !secretKeyBackedUp;
  if (mayClaim(reads.tls)) {
    const tls = reads.tls.value;
    securityLines.push(t("settingsUx.tileTls", { mode: t(TLS_MODE[tls.mode]) }));
    const cert = tls.certificate;
    if (cert && cert.daysRemaining < 30) {
      securityLines.push(t("settingsUx.tileCertExpiring", { left: certTimeLeft(cert, t) }));
      securityAttention = true;
    }
  } else {
    securityLines.push(pendingOf(reads.tls, copy));
  }
  securityLines.push(
    mayClaim(reads.tokens) ? t("settingsUx.tileTokens", { n: reads.tokens.value }) : pendingOf(reads.tokens, copy),
    secretKeyBackedUp ? t("settingsUx.tileBackedUp") : t("settingsUx.tileNotBackedUp"),
  );
  return {
    tab: "security",
    title: "set.tabSecurity",
    lines: securityLines,
    attention: securityAttention,
  };
}

/** The transport card's own words for the time left, so the two never
 *  disagree and "1 days" never appears. */
function certTimeLeft(cert: NonNullable<TlsStatus["certificate"]>, t: Translator): string {
  const n = Math.abs(cert.daysRemaining);
  if (!cert.expired) return t(n === 1 ? "set.certDaysLeftOne" : "set.certDaysLeftMany", { n });
  if (n === 0) return t("set.certExpiredToday");
  return t(n === 1 ? "set.certExpiredAgoOne" : "set.certExpiredAgoMany", { n });
}
