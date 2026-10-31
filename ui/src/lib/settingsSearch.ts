import type { TranslationKey } from "./i18n";

/** The sub-rail's sections, in rail order. The values are the `?tab=` values
 *  the settings page already reads, so a link to a section keeps working. */
export const SETTINGS_TABS = [
  "ingest",
  "pipeline",
  "platforms",
  "overlays",
  "obs",
  "security",
  "danger",
  "debug",
] as const;

export type SettingsTab = (typeof SETTINGS_TABS)[number];

/** One searchable thing on the settings page: a card title or a field label.
 *
 *  `anchor` is the id of the control a field's <Label htmlFor> points at, so a
 *  search result can focus the field itself. Without one the page is scrolled
 *  to the element whose text is the label, which is how card titles and the
 *  few labels that have no single control are reached. `card` names the card a
 *  field sits in, which is what tells the four "Enabled" switches apart. */
export interface SettingsEntry {
  tab: SettingsTab;
  key: TranslationKey;
  card?: TranslationKey;
  anchor?: string;
}

/** Every card title and field label on the settings page, by section.
 *
 *  A STATIC LIST, because Radix mounts only the open tab: the fields of the
 *  other four sections are not in the DOM to be searched. The drift test in
 *  settingsSearch.drift.test.ts holds it to SettingsPage.tsx, so a new label
 *  that is not added here fails the build instead of being unfindable. */
export const SETTINGS_INDEX: readonly SettingsEntry[] = [
  // ingest
  { tab: "ingest", key: "set.ingest" },
  { tab: "ingest", key: "set.mode", card: "set.ingest" },
  { tab: "ingest", key: "set.latencyMs", card: "set.ingest", anchor: "srt-latency" },
  { tab: "ingest", key: "set.passphrase", card: "set.ingest", anchor: "srt-pass" },
  { tab: "ingest", key: "set.app", card: "set.ingest", anchor: "rtmp-app" },
  { tab: "ingest", key: "set.streamKey", card: "set.ingest", anchor: "rtmp-key" },
  { tab: "ingest", key: "set.encoderUrl" },
  { tab: "ingest", key: "set.listeners" },
  { tab: "ingest", key: "set.srtPort", card: "set.listeners", anchor: "listener-srt" },
  { tab: "ingest", key: "set.rtmpPort", card: "set.listeners", anchor: "listener-rtmp" },
  { tab: "ingest", key: "set.srtlaPort", card: "set.listeners", anchor: "listener-srtla" },
  { tab: "ingest", key: "set.ffmpegTitle" },
  // pipeline
  { tab: "pipeline", key: "set.displayTitle" },
  { tab: "pipeline", key: "set.displayTimeZone", card: "set.displayTitle", anchor: "disp-tz" },
  { tab: "pipeline", key: "set.syntheticAudio" },
  { tab: "pipeline", key: "set.synthesiseSilent", card: "set.syntheticAudio", anchor: "synth-silence" },
  { tab: "pipeline", key: "set.goingLive" },
  { tab: "pipeline", key: "set.stagger", card: "set.goingLive", anchor: "dest-stagger" },
  { tab: "pipeline", key: "set.ebTitle" },
  // The GPU rows repeat per GPU, so they are found by text, which lands on the first.
  { tab: "pipeline", key: "automod.modelName", card: "set.ebTitle" },
  { tab: "pipeline", key: "i18nSweep.gpuVendor", card: "set.ebTitle" },
  { tab: "pipeline", key: "i18nSweep.gpuDriver", card: "set.ebTitle" },
  { tab: "pipeline", key: "i18nSweep.gpuDeviceId", card: "set.ebTitle" },
  { tab: "pipeline", key: "i18nSweep.gpuVram", card: "set.ebTitle" },
  { tab: "pipeline", key: "i18nSweep.gpuShared", card: "set.ebTitle" },
  { tab: "pipeline", key: "set.chatHistory" },
  { tab: "pipeline", key: "set.chatHours", card: "set.chatHistory", anchor: "chat-hours" },
  { tab: "pipeline", key: "set.chatKeep", card: "set.chatHistory", anchor: "chat-keep" },
  { tab: "pipeline", key: "set.chatPurge", card: "set.chatHistory", anchor: "chat-purge" },
  { tab: "pipeline", key: "set.chatSendOnConnect", card: "set.chatHistory", anchor: "chat-history" },
  { tab: "pipeline", key: "set.chatYouTubeQuota", card: "set.chatHistory", anchor: "chat-yt-quota" },
  { tab: "pipeline", key: "set.chatYouTubeReserve", card: "set.chatHistory", anchor: "chat-yt-reserve" },
  { tab: "pipeline", key: "automod.rulesTitle" },
  { tab: "pipeline", key: "automod.modelTitle" },
  { tab: "pipeline", key: "set.alertDelivery" },
  { tab: "pipeline", key: "set.deliveryAttempts", card: "set.alertDelivery", anchor: "alert-attempts" },
  { tab: "pipeline", key: "set.failover" },
  { tab: "pipeline", key: "set.enabled", card: "set.failover", anchor: "fo-enabled" },
  { tab: "pipeline", key: "set.gracePeriod", card: "set.failover", anchor: "fo-grace" },
  { tab: "pipeline", key: "set.comingBack", card: "set.failover" },
  { tab: "pipeline", key: "set.returnAfter", card: "set.failover", anchor: "fo-stable" },
  { tab: "pipeline", key: "set.showSlate", card: "set.failover", anchor: "fo-slate" },
  { tab: "pipeline", key: "set.slateImage", card: "set.failover", anchor: "fo-slate-img" },
  { tab: "pipeline", key: "set.slateColour", card: "set.failover", anchor: "fo-slate-col" },
  { tab: "pipeline", key: "set.slateBitrate", card: "set.failover", anchor: "fo-slate-kbps" },
  { tab: "pipeline", key: "set.slateEncoder", card: "set.failover", anchor: "fo-slate-enc" },
  { tab: "pipeline", key: "set.slatePreset", card: "set.failover", anchor: "fo-slate-preset" },
  { tab: "pipeline", key: "set.playLoop", card: "set.failover", anchor: "fo-playlist" },
  { tab: "pipeline", key: "set.mqtt" },
  { tab: "pipeline", key: "set.enabled", card: "set.mqtt", anchor: "mq-enabled" },
  { tab: "pipeline", key: "set.brokerUrl", card: "set.mqtt", anchor: "mq-url" },
  { tab: "pipeline", key: "set.username", card: "set.mqtt", anchor: "mq-user" },
  { tab: "pipeline", key: "set.password", card: "set.mqtt", anchor: "mq-pw" },
  { tab: "pipeline", key: "set.topicPrefix", card: "set.mqtt", anchor: "mq-prefix" },
  { tab: "pipeline", key: "set.instance", card: "set.mqtt", anchor: "mq-inst" },
  { tab: "pipeline", key: "set.publishInterval", card: "set.mqtt", anchor: "mq-int" },
  { tab: "pipeline", key: "set.keepAlive", card: "set.mqtt", anchor: "mq-ka" },
  { tab: "pipeline", key: "set.clientId", card: "set.mqtt", anchor: "mq-cid" },
  { tab: "pipeline", key: "set.haDiscovery", card: "set.mqtt", anchor: "mq-disc" },
  { tab: "pipeline", key: "set.acceptSelfSigned", card: "set.mqtt", anchor: "mq-tls" },
  { tab: "pipeline", key: "set.preview" },
  { tab: "pipeline", key: "set.enabled", card: "set.preview", anchor: "prev-enabled" },
  { tab: "pipeline", key: "set.segmentSec", card: "set.preview", anchor: "prev-seg" },
  { tab: "pipeline", key: "set.height", card: "set.preview", anchor: "prev-h" },
  { tab: "pipeline", key: "set.stopAfterIdle", card: "set.preview", anchor: "prev-idle" },
  { tab: "pipeline", key: "set.audioMeters" },
  { tab: "pipeline", key: "set.enabled", card: "set.audioMeters", anchor: "met-enabled" },
  { tab: "pipeline", key: "set.updateInterval", card: "set.audioMeters", anchor: "met-int" },
  { tab: "pipeline", key: "set.processLogs" },
  { tab: "pipeline", key: "set.writeLogs", card: "set.processLogs", anchor: "log-persist" },
  { tab: "pipeline", key: "set.fileSizeMb", card: "set.processLogs", anchor: "log-mb" },
  { tab: "pipeline", key: "set.filesKept", card: "set.processLogs", anchor: "log-files" },
  // platforms
  { tab: "platforms", key: "set.whatPlatformsDo" },
  { tab: "platforms", key: "set.whyDevApp" },
  { tab: "platforms", key: "set.redirectUri" },
  { tab: "platforms", key: "set.clientId" },
  { tab: "platforms", key: "set.clientSecret" },
  // Each card names its pair in the platform's own words (lib/
  // platformCredentialLabels.ts), so "App ID" has to find the Facebook card.
  { tab: "platforms", key: "credLabels.appId" },
  { tab: "platforms", key: "credLabels.appSecret" },
  { tab: "platforms", key: "credLabels.clientIdentifier" },
  { tab: "platforms", key: "set.connectedAccounts" },
  // obs bridge (#16): its fields live in ObsBridgeSettings.tsx
  { tab: "obs", key: "obs.title" },
  // security
  { tab: "security", key: "set.adminPassword" },
  { tab: "security", key: "set.currentPassword", card: "set.adminPassword", anchor: "cur-pw" },
  { tab: "security", key: "set.newPassword", card: "set.adminPassword", anchor: "new-pw" },
  { tab: "security", key: "set.confirmPassword", card: "set.adminPassword", anchor: "conf-pw" },
  { tab: "security", key: "set.transportSecurity" },
  { tab: "security", key: "set.acmeHostname", card: "set.transportSecurity", anchor: "acme-host" },
  { tab: "security", key: "set.acmeEmailLabel", card: "set.transportSecurity", anchor: "acme-email" },
  { tab: "security", key: "set.apiTokens" },
  { tab: "security", key: "set.name", card: "set.apiTokens", anchor: "tok-name" },
  { tab: "security", key: "set.tokenScope", card: "set.apiTokens", anchor: "tok-scope" },
  // danger zone (A17)
  { tab: "danger", key: "danger.title" },
  // debug: on the System page since A17; a hit navigates there
  { tab: "debug", key: "debug.title" },
];

/** Finds sections whose translated name contains the query. Matching the
 *  label the operator reads, rather than an English keyword list, means a
 *  search works in every locale. */
export function searchSettings(
  query: string,
  labels: Record<SettingsTab, string>,
): SettingsTab[] {
  const needle = query.trim().toLowerCase();
  if (needle === "") return [];
  return SETTINGS_TABS.filter((tab) => labels[tab].toLowerCase().includes(needle));
}

/** A field or card that matched, with the words to show for it. */
export interface SettingsHit {
  tab: SettingsTab;
  /** The matched label, translated. */
  label: string;
  /** The card it sits in, translated, or "" for a card title itself. */
  context: string;
  /** The label's own text, for finding it on the page when there is no id. */
  key: TranslationKey;
  anchor?: string;
}

/** Fields and card titles whose translated text contains the query, in page
 *  order, grouped by section in rail order. Translating at search time is what
 *  makes "Passwort" find the password field in German. A card whose title
 *  matches is listed once, not again for each field under it. */
export function searchSettingsFields(
  query: string,
  translate: (key: TranslationKey) => string,
): SettingsHit[] {
  const needle = query.trim().toLowerCase();
  if (needle === "") return [];
  const hits: SettingsHit[] = [];
  for (const tab of SETTINGS_TABS) {
    for (const entry of SETTINGS_INDEX) {
      if (entry.tab !== tab) continue;
      const label = translate(entry.key);
      if (!label.toLowerCase().includes(needle)) continue;
      hits.push({
        tab,
        label,
        context: entry.card ? translate(entry.card) : "",
        key: entry.key,
        anchor: entry.anchor,
      });
    }
  }
  return hits;
}

/** Whether a keystroke is going into a text field, where `/` is a character
 *  and not the shortcut to the search box. */
export function isTypingTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  if (target.isContentEditable) return true;
  return ["INPUT", "TEXTAREA", "SELECT"].includes(target.tagName);
}

/** Finds the label or card title whose text starts with `text`, inside the
 *  open settings panel. */
function findByText(root: ParentNode, text: string): HTMLElement | null {
  const want = text.trim();
  if (want === "") return null;
  const candidates = root.querySelectorAll<HTMLElement>(
    "[role=tabpanel] label, [role=tabpanel] [data-slot=card-title]",
  );
  for (const el of candidates) {
    if ((el.textContent ?? "").trim().startsWith(want)) return el;
  }
  return null;
}

/** Brings a search hit into view once its section is on screen: focuses the
 *  field when it has a control, otherwise scrolls to the label carrying its
 *  text. When the field is not rendered -- it sits behind a switch that is off,
 *  such as the MQTT fields before MQTT is enabled -- it lands on the field's
 *  card instead, so a result never opens a tab and then does nothing. Field and
 *  card mount together, so the card is only chosen when the field is absent.
 *  Returns whether anything was found, so the caller can try again after the
 *  tab's panel has mounted. */
export function revealSettingsHit(
  hit: Pick<SettingsHit, "anchor" | "label"> & { context?: string },
  root: ParentNode = document,
): boolean {
  let target: HTMLElement | null = null;
  if (hit.anchor) {
    const el = root.querySelector<HTMLElement>(`#${CSS.escape(hit.anchor)}`);
    if (el) {
      el.scrollIntoView?.({ block: "center", behavior: "smooth" });
      el.focus({ preventScroll: true });
      target = el.closest<HTMLElement>("div") ?? el;
    }
  }
  if (!target) {
    target = findByText(root, hit.label) ?? (hit.context ? findByText(root, hit.context) : null);
    target?.scrollIntoView?.({ block: "center", behavior: "smooth" });
  }
  if (!target) return false;
  // A short highlight so the eye lands on the right row after the scroll.
  target.setAttribute("data-search-hit", "");
  window.setTimeout(() => target?.removeAttribute("data-search-hit"), 1600);
  return true;
}
