import { ApiError, PolyemesisClient, type Transport } from "./api.js";
import type { StreamDeckKey } from "./keys.js";
import { ConfigError, keySettings, parseServerUrl, requireToken, type GlobalSettings, type KeySettings } from "./config.js";
import { t } from "./i18n.js";
import { destinationFace, failoverFace, type KeyFace } from "./keyFace.js";
import { HoldToConfirm, type Scheduler } from "./holdToConfirm.js";
import { meterFace } from "./levelsFeed.js";
import type {
  ConnectionInfo,
  DestinationLevels,
  LevelsReading,
  PollSummary,
  RecordingState,
  StatusPayload,
  StopImpact,
  TrackLevel,
  TrackLevelResult,
} from "./model.js";
import { clip, inspectorLines, KEY_STATE, stateFor, stopTitle, titleFor } from "./render.js";
import { barFor, formatDb, gainForDb, gainToDb, stepDb } from "./trackLevel.js";
import { nodeTransport } from "./transport.js";

// The plugin's brain. It owns pairing, the status cache, admin gating and what
// each key does on press. It knows nothing about the Stream Deck SDK: a
// Surface is the one thing the SDK adapter provides per key, and the tests
// supply fakes. That split is what lets every rule here run under vitest.
//
// Two clocks drive it. tick() runs every couple of seconds and refreshes the
// status cache, the stored track levels, and the faces built from them. tickMeters() runs several times a
// second and refreshes the dials from GET /levels, which the server answers
// with its own freshness verdict.

export type SurfaceKind =
  | "destination"
  | "goLive"
  | "stopAll"
  | "stopOne"
  | "failover"
  | "record"
  | "preset"
  | "meter"
  | "trackMute"
  | "trackGain"
  | "poll";

/** Kinds that confirm by a held press. A stop ends broadcasts for good; a preset can restart destinations. */
const HELD_KINDS: readonly SurfaceKind[] = ["stopAll", "stopOne", "preset", "poll"];

/** What an armed hold will act on, captured at key down. A later settings edit cannot retarget it. */
interface Armed {
  destinationId?: number;
  presetId?: string;
  sourceId?: number;
  /** The poll a held poll key ends: the one running at key down. */
  pollId?: number;
}

/** A preset has no broadcast count to load. The hold loader only has to answer so the hold arms. */
const NO_COUNT: StopImpact = { destinations: 0, youtubeBroadcasts: 0 };

/** One key or dial on the device, as the runtime sees it. */
export interface Surface {
  readonly id: string;
  readonly kind: SurfaceKind;
  readonly settings: KeySettings;
  setState(state: number): Promise<void>;
  setTitle(title: string): Promise<void>;
  showAlert(): Promise<void>;
  showOk(): Promise<void>;
  setFeedback(payload: { title: string; value: string; indicator: number }): Promise<void>;
  /**
   * Hands the property inspector a destination key's current issues, one line each. Optional: only a
   * destination key has a list, and the adapter sends it only while that key's inspector is open.
   */
  showIssues?(lines: string[]): Promise<void>;
}

/** The translated message for a failed connection check. The server's own words travel separately. */
export function authKeyFor(err: ApiError): "streamdeck.auth.refused" | "streamdeck.auth.unreachable" {
  if (err.kind === "unauthorized") return "streamdeck.auth.refused";
  return "streamdeck.auth.unreachable";
}

/** Builds the client for a pairing. Throws ConfigError when the pairing is incomplete. */
export function clientFor(globals: GlobalSettings, makeTransport: (ca?: string) => Transport): PolyemesisClient {
  const base = parseServerUrl(globals.url);
  const token = requireToken(globals.token);
  return new PolyemesisClient(base, token, makeTransport(globals.caPem));
}

/** One destination the inspector offers a track control, with the tracks of its selection, named. */
// A type, not an interface, so it is a JsonValue the SDK will send to the inspector.
export type DestinationChoice = {
  id: number;
  name: string;
  tracks: { track: number; name: string }[];
};

/**
 * The destinations of one programme and their tracks, for the inspector's pickers. Only the tracks in
 * a destination's selection are offered: the server answers 404 for any other. A track with no name
 * on the install is named by its 1-based number. Names are a label: if they cannot be read, the list
 * still comes back.
 */
export async function destinationChoices(
  client: PolyemesisClient,
  sourceId: number | undefined,
  locale: string,
): Promise<DestinationChoice[]> {
  const all = await client.destinations();
  const names = await client.trackNames(sourceId).catch(() => new Map<number, string>());
  return all
    .filter((d) => sourceId === undefined || d.sourceId === sourceId)
    .map((d) => ({
      id: d.id,
      name: d.name,
      tracks: d.tracks.map((tr) => ({
        track: tr.track,
        name: names.get(tr.track) ?? t(locale, "streamdeck.track.numbered", { n: tr.track + 1 }),
      })),
    }));
}

const ALL_SCOPE = "*";
function scopeKey(sourceId: number | undefined): string {
  return sourceId === undefined ? ALL_SCOPE : String(sourceId);
}

/** A hold is 1.5 s: long enough that a tap cannot trigger a stop by accident. */
export const HOLD_MS = 1500;

/** How long a key shows how a level change applied (live, or by restart) before its own face returns. */
export const FLASH_MS = 3000;

/**
 * How long the gain dial gathers ticks before it sends. A spin of a second sends about four requests,
 * not one per detent; the server serialises them anyway, so more would only queue behind each other.
 */
export const COALESCE_MS = 250;

/** Kinds that read the install's stored track levels, not a programme's /status. */
const LEVEL_KINDS: readonly SurfaceKind[] = ["trackMute", "trackGain"];

/** A gain dial's turn in progress. */
interface DialTurn {
  /** Where the turns so far have taken the level, in dB. Shown at once; cleared once the server has it. */
  targetDb?: number;
  /** The pending send, while ticks are being gathered. */
  cancel?: () => void;
  /** A request is out. A turn meanwhile waits for it, then the latest level is sent. */
  inFlight: boolean;
}

/** The destination and track a track-level control acts on, or the face that says why it cannot. */
type TrackTarget = { dest: DestinationLevels; level: TrackLevel } | { face: KeyFace };

export class Runtime {
  private globals: GlobalSettings = {};
  private client: PolyemesisClient | undefined;
  private pairingError: ConfigError | undefined;
  private connection: ConnectionInfo | undefined;
  /** Why the install cannot be reached right now. Cleared by a successful check. */
  private connectionError: ApiError | undefined;
  /** A failed status read for one programme. Its keys show unreachable, never the old state. */
  private readonly scopeErrors = new Map<string, ApiError>();
  private readonly surfaces = new Map<string, Surface>();
  private readonly holds = new Map<string, HoldToConfirm>();
  /**
   * Tap keys (mute, the gain dial's push, record) with a request out. A tap reads the state, then writes
   * its opposite: two taps in flight at once both read the same state and write the same flip, so a
   * double tap meant as mute-then-unmute ends muted. A tap while one is out is ignored instead.
   */
  private readonly tapping = new Set<string>();
  /**
   * Orders what the level cache holds. A read takes a ticket when it STARTS; a level change's answer
   * takes one when it arrives. `levelsAt` is the ticket of what the cache holds now, and an answer with
   * an older ticket is not stored: a status tick that started before a dial's answer cannot put the old
   * gain back, and a tick that started before a tap's own read cannot replace what the tap reads.
   */
  private levelsClock = 0;
  private levelsAt = 0;
  private readonly statuses = new Map<string, StatusPayload>();
  private readonly levels = new Map<string, LevelsReading | { error: true }>();
  private readonly lastMeterPaint = new Map<string, string>();
  /** The issue list last handed to each destination key's inspector, so an unchanged list is not re-sent. */
  private readonly lastIssues = new Map<string, string[]>();
  /** The install-wide recording state, read each tick while a Record key is on the deck. */
  private recordingState: RecordingState | undefined;
  /** The running chat poll: null when none runs, undefined until read. */
  private pollState: PollSummary | null | undefined;
  private pollError: ApiError | undefined;
  private recordingError: ApiError | undefined;
  /** Preset id to name, from the install's list. Undefined until a read succeeds. */
  private presetNames: Map<string, string> | undefined;
  /** The target of each armed hold. See Armed. */
  private readonly armed = new Map<string, Armed>();
  /** Keys whose last preset apply was refused for want of a programme. Cleared by the next press or a success. */
  private readonly presetRefused = new Set<string>();
  /** One status refresh at a time. A tick that arrives while one is in flight is skipped, not queued. */
  private statusBusy = false;
  /** One meter poll at a time, for the same reason. A dropped packet must not stack requests. */
  private meterBusy = false;
  /** Every destination's stored track levels, by id, read each tick while a track control is on the deck. */
  private destLevels: Map<number, DestinationLevels> | undefined;
  private destLevelsError: ApiError | undefined;
  /** Track names per programme scope. A failed read keeps the last names; a number stands in for none. */
  private readonly trackNames = new Map<string, Map<number, string>>();
  /**
   * A key showing how its last change applied. The periodic repaint leaves it alone until the
   * timer clears it, so a restart is not painted over before the operator has seen it.
   */
  private readonly flashes = new Map<string, () => void>();
  /** A track control the server refused (409, 400, 404), with its words. Cleared by the next press. */
  private readonly levelRefused = new Map<string, KeyFace>();
  /** Each gain dial's turn in progress. */
  private readonly turns = new Map<string, DialTurn>();
  /** What a gain dial says for FLASH_MS instead of its level: how a change applied, or needs admin. */
  private readonly dialNotices = new Map<string, string>();

  constructor(
    private readonly locale: string,
    private readonly makeTransport: (ca?: string) => Transport = nodeTransport,
    private readonly schedule: Scheduler = (fn, ms) => {
      const handle = setTimeout(fn, ms);
      return () => clearTimeout(handle);
    },
  ) {}

  // ---- pairing -----------------------------------------------------------

  configure(globals: GlobalSettings): void {
    this.globals = globals;
    this.connection = undefined;
    this.connectionError = undefined;
    this.pairingError = undefined;
    this.statuses.clear();
    this.scopeErrors.clear();
    this.levels.clear();
    this.recordingState = undefined;
    this.recordingError = undefined;
    this.pollState = undefined;
    this.pollError = undefined;
    this.presetNames = undefined;
    this.presetRefused.clear();
    this.destLevels = undefined;
    this.destLevelsError = undefined;
    this.trackNames.clear();
    this.levelRefused.clear();
    for (const cancel of this.flashes.values()) cancel();
    this.flashes.clear();
    this.dialNotices.clear();
    for (const turn of this.turns.values()) turn.cancel?.();
    this.turns.clear();
    this.client = undefined;
    try {
      this.client = clientFor(globals, this.makeTransport);
    } catch (err) {
      if (!(err instanceof ConfigError)) throw err;
      this.pairingError = err;
    }
  }

  /** The connection check: what this token is and whether it may run write keys. */
  async verify(): Promise<ConnectionInfo> {
    if (!this.client) throw this.pairingError ?? new ConfigError("url", "streamdeck.err.urlRequired");
    try {
      this.connection = await this.client.connection();
      this.connectionError = undefined;
      return this.connection;
    } catch (err) {
      this.connection = undefined;
      this.connectionError = err instanceof ApiError ? err : new ApiError("unreachable", undefined, String(err));
      throw this.connectionError;
    }
  }

  /** The catalog this runtime paints in. */
  get language(): string {
    return this.locale;
  }

  get isAdmin(): boolean {
    return this.connection?.adminScope === true;
  }

  /** A held key's face while its hold is loading or armed. The periodic repaint must not overwrite it. */
  private holdInProgress(id: string): boolean {
    const phase = this.holds.get(id)?.current;
    return phase === "loading" || phase === "arming";
  }

  add(surface: Surface): void {
    this.surfaces.set(surface.id, surface);
    if (HELD_KINDS.includes(surface.kind)) {
      this.holds.set(
        surface.id,
        new HoldToConfirm(HOLD_MS, this.schedule, () => void this.confirmHeld(surface)),
      );
    }
  }

  private hasKind(kind: SurfaceKind): boolean {
    for (const s of this.surfaces.values()) if (s.kind === kind) return true;
    return false;
  }

  remove(id: string): void {
    this.holds.get(id)?.release();
    this.holds.delete(id);
    this.surfaces.delete(id);
    this.lastMeterPaint.delete(id);
    this.lastIssues.delete(id);
    this.flashes.get(id)?.();
    this.flashes.delete(id);
    this.levelRefused.delete(id);
    this.dialNotices.delete(id);
    this.turns.get(id)?.cancel?.();
    this.turns.delete(id);
  }

  // ---- status clock ------------------------------------------------------

  /** Refreshes the status cache and repaints every key. Runs every couple of seconds. */
  async tick(): Promise<void> {
    if (this.statusBusy) return;
    this.statusBusy = true;
    try {
      if (!this.client) {
        await this.paintAll();
        return;
      }
      // A bad token is not retried: each attempt is a credential check the install logs.
      // An unreachable install is retried on every tick, so keys recover when it returns.
      if (!this.connection && this.connectionError?.kind !== "unauthorized") {
        await this.verify().catch(() => undefined);
      }
      if (!this.connection) {
        await this.paintAll();
        return;
      }
      await this.refreshStatuses();
      if (this.hasKind("record")) await this.refreshRecording();
      if (this.hasKind("poll")) await this.refreshPoll();
      if (this.hasKind("preset")) await this.loadPresetNames();
      if (LEVEL_KINDS.some((k) => this.hasKind(k))) await this.refreshLevels();
      await this.paintAll();
    } finally {
      this.statusBusy = false;
    }
  }

  /** The Record key's answer: install-wide, so it is one read, not one per programme. */
  private async refreshRecording(): Promise<void> {
    try {
      this.recordingState = await this.client!.recording();
      this.recordingError = undefined;
    } catch (err) {
      this.recordingState = undefined;
      const apiErr = err instanceof ApiError ? err : new ApiError("unreachable", undefined, String(err));
      this.recordingError = apiErr;
      if (apiErr.kind === "unauthorized" || apiErr.kind === "untrusted") {
        this.connection = undefined;
        this.connectionError = apiErr;
      }
    }
  }

  /** The poll key's answer: install-wide, one read. */
  private async refreshPoll(): Promise<void> {
    try {
      this.pollState = await this.client!.currentPoll();
      this.pollError = undefined;
    } catch (err) {
      this.pollState = undefined;
      const apiErr = err instanceof ApiError ? err : new ApiError("unreachable", undefined, String(err));
      this.pollError = apiErr;
      if (apiErr.kind === "unauthorized" || apiErr.kind === "untrusted") {
        this.connection = undefined;
        this.connectionError = apiErr;
      }
    }
  }

  /**
   * The stored track levels of every destination, one install-wide read, and the track names of each
   * programme a track control is bound to. A mute made in the UI or over the API shows on the next tick.
   */
  private async refreshLevels(): Promise<void> {
    try {
      await this.readLevels();
      this.destLevelsError = undefined;
    } catch (err) {
      // The old levels are not evidence of the present: a key must not show "unmuted" from them.
      this.destLevels = undefined;
      const apiErr = err instanceof ApiError ? err : new ApiError("unreachable", undefined, String(err));
      this.destLevelsError = apiErr;
      if (apiErr.kind === "unauthorized" || apiErr.kind === "untrusted") {
        this.connection = undefined;
        this.connectionError = apiErr;
      }
      return;
    }
    const scopes = new Set<string>();
    for (const s of this.surfaces.values()) if (LEVEL_KINDS.includes(s.kind)) scopes.add(scopeKey(s.settings.sourceId));
    for (const key of scopes) {
      try {
        this.trackNames.set(key, await this.client!.trackNames(key === ALL_SCOPE ? undefined : Number(key)));
      } catch {
        // A name is a label, not a state. Without one the key names the track by number.
      }
    }
  }

  private storeLevels(list: DestinationLevels[], at: number): void {
    this.destLevels = new Map(list.map((d) => [d.id, d]));
    this.levelsAt = at;
  }

  /**
   * Reads every destination's stored levels and caches them, unless the cache already holds something
   * newer than this read: a read that started later, or a level change answered after this one started.
   * Ordered by when the read STARTED, not by when it answered, so a slow older read never wins.
   */
  private async readLevels(): Promise<void> {
    const at = ++this.levelsClock;
    const list = await this.client!.destinations();
    // A cache dropped meanwhile (a failed read) holds nothing to keep, so it is refilled.
    if (this.destLevels && at < this.levelsAt) return;
    this.storeLevels(list, at);
  }

  /** The preset names, read once. A key shows the id until the list arrives. */
  private async loadPresetNames(): Promise<void> {
    if (this.presetNames !== undefined) return;
    try {
      const list = await this.client!.routingPresets();
      this.presetNames = new Map(list.map((p) => [p.id, p.name]));
    } catch {
      // Left undefined: the next tick asks again.
    }
  }

  private async refreshStatuses(): Promise<void> {
    const scopes = new Set<string>();
    // Install-wide and preset keys do not read a programme's status. Their own reads answer for them.
    for (const s of this.surfaces.values()) {
      if (s.kind === "meter" || s.kind === "record" || s.kind === "preset" || s.kind === "poll" || LEVEL_KINDS.includes(s.kind)) continue;
      scopes.add(scopeKey(s.settings.sourceId));
    }
    for (const key of scopes) {
      try {
        const sourceId = key === ALL_SCOPE ? undefined : Number(key);
        this.statuses.set(key, (await this.client!.status(sourceId)) as StatusPayload);
        this.scopeErrors.delete(key);
      } catch (err) {
        // The old answer is not evidence of the present. Drop it and say unreachable.
        this.statuses.delete(key);
        const apiErr = err instanceof ApiError ? err : new ApiError("unreachable", undefined, String(err));
        this.scopeErrors.set(key, apiErr);
        if (apiErr.kind === "unauthorized" || apiErr.kind === "untrusted") {
          this.connection = undefined;
          this.connectionError = apiErr;
        }
      }
    }
  }

  // ---- meter clock -------------------------------------------------------

  /**
   * Refreshes each programme's levels and repaints the dials. Runs several times a second.
   * With no connection the dials are repainted as NO FEED, so they never hold a bar that
   * the install can no longer vouch for.
   */
  async tickMeters(): Promise<void> {
    if (this.meterBusy) return;
    this.meterBusy = true;
    try {
      if (!this.client || !this.connection) {
        this.levels.clear();
        await this.paintMeters();
        return;
      }
      const scopes = new Set<string>();
      for (const s of this.surfaces.values()) if (s.kind === "meter") scopes.add(scopeKey(s.settings.sourceId));
      for (const key of scopes) {
        try {
          const sourceId = key === ALL_SCOPE ? undefined : Number(key);
          this.levels.set(key, await this.client.levels(sourceId));
        } catch (err) {
          // No answer is NO FEED, not the last reading held on screen. A refused token
          // here matters as much as on /status, and a dial-only deck never polls /status,
          // so this is where a revoked token is noticed.
          this.levels.set(key, { error: true });
          if (err instanceof ApiError && (err.kind === "unauthorized" || err.kind === "untrusted")) {
            this.connection = undefined;
            this.connectionError = err;
          }
        }
      }
      await this.paintMeters();
    } finally {
      this.meterBusy = false;
    }
  }

  // ---- painting ----------------------------------------------------------

  private async paintAll(): Promise<void> {
    for (const s of this.surfaces.values()) {
      if (s.kind === "meter") continue;
      if (this.holds.has(s.id) && this.holdInProgress(s.id)) continue;
      if (this.flashes.has(s.id)) continue;
      if (s.kind === "trackGain") {
        await this.paintGainDial(s);
        continue;
      }
      await this.paintFace(s, this.faceFor(s));
    }
  }

  /** What a key shows right now, from the cached answers. The one place a key face is decided. */
  private faceFor(s: Surface): KeyFace {
    if (!this.client) return { tone: "idle", label: "streamdeck.auth.notPaired" };
    if (this.connectionError || !this.connection) return this.unavailableFace();
    if (s.kind === "record") return this.recordFace();
    if (s.kind === "poll") return this.pollFace(s);
    if (s.kind === "preset") return this.presetFace(s);
    if (s.kind === "trackMute") return this.trackMuteFace(s);
    const key = scopeKey(s.settings.sourceId);
    const scopeErr = this.scopeErrors.get(key);
    const payload = this.statuses.get(key);
    if (scopeErr || !payload) return this.unavailableFace(scopeErr);
    return this.programmeFace(s, payload);
  }

  /** The face of a key that reads one programme's status. Every kind that does is named here. */
  private programmeFace(s: Surface, payload: StatusPayload): KeyFace {
    switch (s.kind) {
      case "destination": {
        if (s.settings.destinationId === undefined) return { tone: "unbound", label: "streamdeck.state.missing" };
        const dest = payload.destinations?.find((d) => d.id === s.settings.destinationId);
        // The payload's failover is source.id's: placed on a destination only when it is that programme's.
        return destinationFace(dest, s.settings.sourceId, payload.destinationHold, {
          sourceId: payload.source?.id,
          failover: payload.failover,
        });
      }
      case "failover":
        return failoverFace(payload.failover);
      case "goLive":
        return this.adminGated("streamdeck.go.live");
      case "stopAll":
        return this.adminGated("streamdeck.stop.hold");
      case "stopOne":
        return this.stopOneFace(s, payload);
      default:
        return this.unavailableFace();
    }
  }

  /**
   * A per-destination stop key names the destination it will stop, and is unbound when that
   * destination is not on this key's programme. It never shows the destination's state as if
   * the key were the destination.
   */
  private stopOneFace(s: Surface, payload: StatusPayload): KeyFace {
    if (s.settings.destinationId === undefined) return { tone: "unbound", label: "streamdeck.state.missing" };
    const dest = payload.destinations?.find((d) => d.id === s.settings.destinationId);
    if (!dest) return { tone: "unbound", label: "streamdeck.state.missing" };
    if (s.settings.sourceId !== undefined && dest.sourceId !== s.settings.sourceId) {
      return { tone: "unbound", label: "streamdeck.state.otherProgramme" };
    }
    const face = this.adminGated("streamdeck.stop.hold");
    return face.tone === "idle" ? { ...face, detail: dest.name } : face;
  }

  /** The Record key. Install-wide: the same answer on every programme, so the face says what the switch is. */
  private recordFace(): KeyFace {
    if (this.recordingError || !this.recordingState) return this.unavailableFace(this.recordingError);
    const r = this.recordingState;
    switch (r.state) {
      case "off":
        return { tone: "idle", label: "streamdeck.record.off" };
      case "recording":
        return { tone: "live", label: "streamdeck.record.on" };
      case "not_running":
        return { tone: "warn", label: "streamdeck.record.notRunning" };
      case "halted_disk":
        return { tone: "halted", label: "streamdeck.record.haltedDisk", detail: r.reason };
      default:
        return this.unavailableFace();
    }
  }

  /**
   * The poll key. While a poll runs it shows the votes so far and the time left -- the same counts the
   * dashboard has, no voter -- and a hold ends it. With none running it is ready to start the poll its
   * settings describe, or says those settings are missing.
   */
  private pollFace(s: Surface): KeyFace {
    if (this.pollError || this.pollState === undefined) return this.unavailableFace(this.pollError);
    const p = this.pollState;
    if (p && (p.state === "open" || p.state === "closing")) {
      const votes = t(this.locale, "streamdeck.poll.votes", { count: p.total, time: pollClock(p.endsAt, Date.now()) });
      if (p.state === "closing") return { tone: "warn", label: "streamdeck.poll.closing", detail: votes };
      return { tone: "live", label: "streamdeck.poll.live", detail: votes };
    }
    if (!pollSpecOf(s)) return { tone: "unbound", label: "streamdeck.poll.notSet" };
    const face = this.adminGated("streamdeck.poll.start");
    return face.tone === "idle" ? { ...face, detail: s.settings.pollQuestion } : face;
  }

  /** The preset key: unbound, no ingest after a refused apply, needs admin, or ready to hold. */
  private presetFace(s: Surface): KeyFace {
    const presetId = s.settings.presetId;
    if (presetId === undefined) return { tone: "unbound", label: "streamdeck.state.missing" };
    if (this.presetRefused.has(s.id)) return { tone: "error", label: "streamdeck.preset.noIngest" };
    const face = this.adminGated("streamdeck.preset.hold");
    return face.tone === "idle" ? { ...face, detail: this.presetNames?.get(presetId) ?? presetId } : face;
  }

  /**
   * What a track control acts on, from the cached levels: the key's destination, on the key's programme,
   * and the key's track in that destination's selection. Anything else is a face that says what is missing.
   */
  private trackTarget(s: Surface, levels: Map<number, DestinationLevels>): TrackTarget {
    const { destinationId, track, sourceId } = s.settings;
    if (destinationId === undefined || track === undefined) return { face: { tone: "unbound", label: "streamdeck.state.missing" } };
    const dest = levels.get(destinationId);
    if (!dest) return { face: { tone: "unbound", label: "streamdeck.state.missing" } };
    // The same rule the destination key uses: a key bound to programme A never acts on a row of B.
    if (sourceId !== undefined && dest.sourceId !== sourceId) {
      return { face: { tone: "unbound", label: "streamdeck.state.otherProgramme" } };
    }
    const level = dest.tracks.find((t) => t.track === track);
    if (!level) return { face: { tone: "unbound", label: "streamdeck.mute.noTrack" } };
    return { dest, level };
  }

  /** The name the operator knows a track by: the label or title from GET /source, else its 1-based number. */
  private trackName(s: Surface, track: number): string {
    return this.trackNames.get(scopeKey(s.settings.sourceId))?.get(track) ?? t(this.locale, "streamdeck.track.numbered", { n: track + 1 });
  }

  /**
   * The Mute track key: the STORED mute, so a mute made in the UI or over the API shows here too. It shows
   * the state on a read token as well, as the Record key does; only a press needs admin.
   */
  private trackMuteFace(s: Surface): KeyFace {
    if (this.destLevelsError || !this.destLevels) return this.unavailableFace(this.destLevelsError);
    const target = this.trackTarget(s, this.destLevels);
    if ("face" in target) return target.face;
    const refused = this.levelRefused.get(s.id);
    if (refused) return refused;
    if (target.dest.copyAudio) return { tone: "unavailable", label: "streamdeck.mute.unavailable", warning: "streamdeck.mute.copyAudio" };
    const detail = this.trackName(s, target.level.track);
    return target.level.muted
      ? { tone: "muted", label: "streamdeck.mute.muted", detail }
      : { tone: "live", label: "streamdeck.mute.unmuted", detail };
  }

  /**
   * The gain dial's touch strip: the destination and track, the level in dB and a bar. A turn in progress
   * is shown at once, and the status tick does not overwrite it. NO FEED when the stored levels cannot be
   * read; "matrix: use UI" on a matrix mix, whose gain is per cell.
   */
  private gainDialFeedback(s: Surface): { title: string; value: string; indicator: number } {
    const blank = (value: string) => ({ title: "", value, indicator: 0 });
    if (!this.client) return blank(t(this.locale, "streamdeck.auth.notPaired"));
    if (this.connectionError || !this.connection || this.destLevelsError || !this.destLevels) {
      return blank(t(this.locale, "streamdeck.meter.noFeed"));
    }
    const target = this.trackTarget(s, this.destLevels);
    if ("face" in target) return blank(t(this.locale, target.face.label));
    const title = clip(`${target.dest.name} · ${this.trackName(s, target.level.track)}`, 24);
    const notice = this.dialNotices.get(s.id);
    const refused = this.levelRefused.get(s.id);
    const muted = t(this.locale, "streamdeck.mute.muted");
    if (notice !== undefined) return { title, value: notice, indicator: 0 };
    if (refused) return { title, value: t(this.locale, refused.label), indicator: 0 };
    if (target.dest.copyAudio) return { title, value: t(this.locale, "streamdeck.mute.unavailable"), indicator: 0 };
    if (target.dest.matrix) {
      return { title, value: target.level.muted ? muted : t(this.locale, "streamdeck.gain.matrix"), indicator: 0 };
    }
    const db = this.turns.get(s.id)?.targetDb ?? gainToDb(target.level.gain);
    const level = formatDb(db);
    return { title, value: target.level.muted ? `${muted} · ${level}` : level, indicator: barFor(db) };
  }

  private async paintGainDial(s: Surface): Promise<void> {
    const payload = this.gainDialFeedback(s);
    // As the meters do: send only a change, so the 2 s tick does not re-send a steady strip.
    const signature = JSON.stringify(payload);
    if (this.lastMeterPaint.get(s.id) === signature) return;
    this.lastMeterPaint.set(s.id, signature);
    await s.setFeedback(payload);
  }

  /** Shows a word on a gain dial for FLASH_MS, then its level again. */
  private async noticeOnDial(s: Surface, text: string): Promise<void> {
    this.dialNotices.set(s.id, text);
    this.flashes.get(s.id)?.();
    await this.paintGainDial(s);
    this.flashes.set(
      s.id,
      this.schedule(() => {
        this.flashes.delete(s.id);
        this.dialNotices.delete(s.id);
        const current = this.surfaces.get(s.id);
        if (current) void this.paintGainDial(current);
      }, FLASH_MS),
    );
  }

  private clearDialNotice(id: string): void {
    this.flashes.get(id)?.();
    this.flashes.delete(id);
    this.dialNotices.delete(id);
  }

  private adminGated(label: StreamDeckKey): KeyFace {
    return this.isAdmin
      ? { tone: "idle", label }
      : { tone: "needsAdmin", label: "streamdeck.auth.needsAdmin" };
  }

  private unavailableFace(scopeErr?: ApiError): KeyFace {
    const err = scopeErr ?? this.connectionError;
    if (!err) return { tone: "error", label: "streamdeck.auth.unreachable" };
    return { tone: "error", label: authKeyFor(err), detail: err.message };
  }

  private async paintFace(s: Surface, face: KeyFace): Promise<void> {
    await s.setState(stateFor(face.tone));
    await s.setTitle(titleFor(face, this.locale));
    if (s.kind === "destination") await this.sendIssues(s, inspectorLines(face, this.locale));
  }

  /** Hands a destination key's inspector its issue list when the list has changed. */
  private async sendIssues(s: Surface, lines: string[]): Promise<void> {
    const last = this.lastIssues.get(s.id);
    if (last && last.length === lines.length && last.every((l, i) => l === lines[i])) return;
    this.lastIssues.set(s.id, lines);
    await s.showIssues?.(lines);
  }

  /** The issue list a destination key's inspector asked for on opening: the one last painted. */
  issuesFor(id: string): string[] | undefined {
    return this.lastIssues.get(id);
  }

  private async paintMeters(): Promise<void> {
    for (const s of this.surfaces.values()) {
      if (s.kind !== "meter") continue;
      const entry = this.levels.get(scopeKey(s.settings.sourceId));
      const reading = entry && !("error" in entry) ? entry : undefined;
      const face = reading ? meterFace(reading.feed, reading.levels) : meterFace("none", null);
      const value =
        face.peakDb !== undefined ? `${face.peakDb.toFixed(0)} dB` : t(this.locale, face.label ?? "streamdeck.meter.noFeed");
      const payload = { title: "", value, indicator: Math.round(face.value * 100) };
      // Send only when the face changed, so a steady bar is not re-sent several times a second.
      const signature = JSON.stringify(payload);
      if (this.lastMeterPaint.get(s.id) === signature) continue;
      this.lastMeterPaint.set(s.id, signature);
      await s.setFeedback(payload);
    }
  }

  // ---- presses -----------------------------------------------------------

  /** A destination key starts its destination when it is off. Stopping is the stop key's job. */
  async pressDestination(id: string): Promise<void> {
    const s = this.surfaces.get(id);
    if (!s || !this.client || s.settings.destinationId === undefined) return;
    if (!(await this.requireAdmin(s))) return;
    const dest = this.statuses.get(scopeKey(s.settings.sourceId))?.destinations?.find((d) => d.id === s.settings.destinationId);
    if (!dest || dest.enabled) return;
    await this.run(s, () => this.client!.startDestination(s.settings.destinationId!, s.settings.sourceId));
  }

  async pressGoLive(id: string): Promise<void> {
    const s = this.surfaces.get(id);
    if (!s || !this.client) return;
    if (!(await this.requireAdmin(s))) return;
    await this.run(s, () => this.client!.startAll(s.settings.sourceId));
  }

  /** Key down on the stop-all key. Loads the count fresh and arms the hold. */
  async pressStopDown(id: string): Promise<void> {
    const s = this.surfaces.get(id);
    if (!s || !this.client) return;
    if (!(await this.requireAdmin(s))) return;
    this.armed.set(id, { sourceId: s.settings.sourceId });
    await this.armStop(s, () => this.client!.stopImpact(s.settings.sourceId));
  }

  /**
   * Key down on a per-destination stop key. The destination is read NOW and held in the armed
   * target, so the stop that fires later is the one that was counted, whatever the settings say then.
   */
  async pressStopOneDown(id: string): Promise<void> {
    const s = this.surfaces.get(id);
    if (!s || !this.client) return;
    if (!(await this.requireAdmin(s))) return;
    // The same ownership rule the face uses: the destination must be on this key's programme, as the
    // last status read says. The server ignores ?source= on a per-destination stop, so this check is the
    // only thing that keeps the key from stopping a destination the face says it does not own.
    const destinationId = s.settings.destinationId;
    const owned = destinationId !== undefined && this.ownsDestination(s, destinationId);
    if (destinationId === undefined || !owned) {
      await s.setTitle(t(this.locale, "streamdeck.state.missing"));
      await s.showAlert();
      return;
    }
    this.armed.set(id, { destinationId, sourceId: s.settings.sourceId });
    await this.armStop(s, () => this.client!.stopImpactFor(destinationId, s.settings.sourceId));
  }

  /**
   * True when the last status read for this key's programme lists the destination on that programme.
   * With no status yet, or a status that does not list it, nothing is owned.
   */
  private ownsDestination(s: Surface, destinationId: number): boolean {
    const payload = this.statuses.get(scopeKey(s.settings.sourceId));
    const dest = payload?.destinations?.find((d) => d.id === destinationId);
    if (!dest) return false;
    return s.settings.sourceId === undefined || dest.sourceId === s.settings.sourceId;
  }

  /** Loads the count through the hold. A refused count is refused here, and nothing arms. */
  private async armStop(s: Surface, load: () => Promise<StopImpact>): Promise<void> {
    const hold = this.holds.get(s.id);
    if (!hold) return;
    const result = await hold.press(load);
    // A repeat, or a load that answered after a newer press: the face is the newer press's to paint.
    if (result.superseded) return;
    if (result.phase === "refused") {
      await s.setTitle(stopTitle(undefined, this.locale));
      await s.showAlert();
    } else if (result.phase === "arming") {
      await s.setState(KEY_STATE.held);
      await s.setTitle(stopTitle(result.impact?.youtubeBroadcasts, this.locale));
    }
  }

  /** Key down on a preset key. Arms the hold on the preset, read now. */
  async pressPresetDown(id: string): Promise<void> {
    const s = this.surfaces.get(id);
    const hold = this.holds.get(id);
    if (!s || !hold || !this.client) return;
    if (!(await this.requireAdmin(s))) return;
    const presetId = s.settings.presetId;
    if (presetId === undefined) {
      await s.setTitle(t(this.locale, "streamdeck.state.missing"));
      await s.showAlert();
      return;
    }
    this.presetRefused.delete(id);
    this.armed.set(id, { presetId, sourceId: s.settings.sourceId });
    const result = await hold.press(async () => NO_COUNT);
    if (!result.superseded && result.phase === "arming") {
      await s.setState(KEY_STATE.held);
      await s.setTitle(
        titleFor(
          { tone: "held", label: "streamdeck.preset.applying", detail: this.presetNames?.get(presetId) ?? presetId },
          this.locale,
        ),
      );
    }
  }

  /**
   * Key down on the poll key. Reads the running poll FRESH: with one running, the press arms a hold that
   * ends that poll (captured now, so a poll started meanwhile is not the one ended); with none, a tap
   * starts the key's poll.
   */
  async pressPollDown(id: string): Promise<void> {
    const s = this.surfaces.get(id);
    const hold = this.holds.get(id);
    if (!s || !hold || !this.client) return;
    if (!(await this.requireAdmin(s))) return;
    let current: PollSummary | null;
    try {
      current = await this.client.currentPoll();
      this.pollState = current;
      this.pollError = undefined;
    } catch {
      await s.showAlert();
      return;
    }
    if (current && (current.state === "open" || current.state === "closing")) {
      this.armed.set(id, { pollId: current.id });
      const result = await hold.press(async () => NO_COUNT);
      if (!result.superseded && result.phase === "arming") {
        await s.setState(KEY_STATE.held);
        await s.setTitle(titleFor({ tone: "held", label: "streamdeck.poll.holdEnd", detail: current.question }, this.locale));
      }
      return;
    }
    const spec = pollSpecOf(s);
    if (!spec) {
      await s.setTitle(t(this.locale, "streamdeck.poll.notSet"));
      await s.showAlert();
      return;
    }
    await this.oneTap(id, async () => {
      try {
        await this.client!.startPoll(spec);
        this.pollState = await this.client!.currentPoll().catch(() => undefined);
        await s.showOk();
      } catch {
        await s.showAlert();
        return;
      }
      await this.paintFace(s, this.pollFace(s));
    });
  }

  /** Key down on the record key. Reads the switch fresh, then flips it. Tap, not hold: see README. */
  async pressRecord(id: string): Promise<void> {
    await this.oneTap(id, () => this.flipRecording(id));
  }

  private async flipRecording(id: string): Promise<void> {
    const s = this.surfaces.get(id);
    if (!s || !this.client) return;
    if (!(await this.requireAdmin(s))) return;
    try {
      const current = await this.client.recording();
      this.recordingState = await this.client.setRecording(!current.enabled);
      this.recordingError = undefined;
      await s.showOk();
    } catch {
      await s.showAlert();
      return;
    }
    await this.paintFace(s, this.recordFace());
  }

  /**
   * Key up, or the key leaving the screen: cancels any pending confirmation and repaints the key's
   * own face. Not a fixed idle face: a stop-one key names its destination, and a read token's key
   * says it needs admin.
   */
  async pressHoldUp(id: string): Promise<void> {
    const s = this.surfaces.get(id);
    this.holds.get(id)?.release();
    if (!s) return;
    await this.paintFace(s, this.faceFor(s));
  }

  /** Runs when a hold completes. Acts on what was armed at key down, never on the settings as they are now. */
  private async confirmHeld(s: Surface): Promise<void> {
    const target = this.armed.get(s.id);
    if (!target || !this.client) return;
    if (s.kind === "stopAll") {
      await this.run(s, () => this.client!.stopAll(target.sourceId));
    } else if (s.kind === "stopOne" && target.destinationId !== undefined) {
      await this.run(s, () => this.client!.stopDestination(target.destinationId!, target.sourceId));
    } else if (s.kind === "preset" && target.presetId !== undefined) {
      await this.applyArmedPreset(s, target.presetId, target.sourceId);
    } else if (s.kind === "poll" && target.pollId !== undefined) {
      await this.run(s, () => this.client!.endPoll(target.pollId!));
    }
  }

  private async applyArmedPreset(s: Surface, presetId: string, sourceId: number | undefined): Promise<void> {
    try {
      await this.client!.applyPreset(presetId, sourceId);
      this.presetRefused.delete(s.id);
      await s.showOk();
    } catch (err) {
      if (err instanceof ApiError && err.kind === "noSource") {
        this.presetRefused.add(s.id);
        await this.paintFace(s, this.presetFace(s));
      }
      await s.showAlert();
    }
  }

  /**
   * The Mute track key. Reads the stored levels fresh, then flips the track's mute. Tap, not hold: a mute
   * is undone by the next tap and ends nothing.
   */
  async pressTrackMute(id: string): Promise<void> {
    await this.oneTap(id, () => this.flipTrackMute(id));
  }

  private async flipTrackMute(id: string): Promise<void> {
    const s = this.surfaces.get(id);
    if (!s || !this.client) return;
    if (!(await this.requireAdmin(s))) return;
    const target = await this.freshTarget(s);
    if (!target) return;
    const result = await this.sendLevel(s, target.dest, target.level.track, { muted: !target.level.muted });
    if (result) await this.flashApplied(s, result);
  }

  /**
   * A mute toggle reads the stored levels FRESH, so it flips what the install has now, not what the last
   * tick saw. Undefined (after an alert) when the read fails, the target is missing, or the destination
   * passes its audio through: refused here, so a control never reports a mute the audio does not have.
   */
  private async freshTarget(s: Surface): Promise<{ dest: DestinationLevels; level: TrackLevel } | undefined> {
    this.levelRefused.delete(s.id);
    try {
      await this.readLevels();
      this.destLevelsError = undefined;
    } catch {
      await s.showAlert();
      return undefined;
    }
    const target = this.trackTarget(s, this.destLevels!);
    if ("face" in target || target.dest.copyAudio) {
      await this.repaintTrackControl(s);
      await s.showAlert();
      return undefined;
    }
    return target;
  }

  private async repaintTrackControl(s: Surface): Promise<void> {
    if (s.kind === "trackGain") await this.paintGainDial(s);
    else await this.paintFace(s, this.trackMuteFace(s));
  }

  /** The admin gate for the gain dial: nothing is sent, and the strip says why. */
  private async dialNeedsAdmin(s: Surface): Promise<boolean> {
    if (this.isAdmin) return true;
    await this.noticeOnDial(s, t(this.locale, "streamdeck.auth.needsAdmin"));
    await s.showAlert();
    return false;
  }

  /**
   * The cached target a gain change may go to: a track in the selection, on a destination that mixes its
   * audio and is not a matrix mix (the server refuses a track gain there). Undefined after an alert.
   */
  private async gainTarget(s: Surface): Promise<{ dest: DestinationLevels; level: TrackLevel } | undefined> {
    this.levelRefused.delete(s.id);
    const target = this.destLevels ? this.trackTarget(s, this.destLevels) : undefined;
    if (!target || "face" in target || target.dest.copyAudio || target.dest.matrix) {
      await this.paintGainDial(s);
      await s.showAlert();
      return undefined;
    }
    return target;
  }

  private turnFor(id: string): DialTurn {
    let turn = this.turns.get(id);
    if (!turn) this.turns.set(id, (turn = { inFlight: false }));
    return turn;
  }

  /**
   * A turn of the gain dial: 0.5 dB a tick, clamped. The level moves on the strip at once; the request
   * goes COALESCE_MS later, carrying wherever the turns have got to by then, so a fast spin sends a few
   * requests, not one per tick.
   */
  async rotateTrackGain(id: string, ticks: number): Promise<void> {
    const s = this.surfaces.get(id);
    if (!s || !this.client) return;
    if (!(await this.dialNeedsAdmin(s))) return;
    const target = await this.gainTarget(s);
    if (!target) return;
    this.clearDialNotice(id);
    const turn = this.turnFor(id);
    const from = turn.targetDb ?? gainToDb(target.level.gain);
    const next = stepDb(from, ticks);
    if (next === from) return;
    turn.targetDb = next;
    await this.paintGainDial(s);
    turn.cancel ??= this.schedule(() => {
      turn.cancel = undefined;
      void this.flushGain(id);
    }, COALESCE_MS);
  }

  /** A touch on the strip: back to unity (0 dB) at once, replacing any turn not yet sent. */
  async touchTrackGain(id: string): Promise<void> {
    const s = this.surfaces.get(id);
    if (!s || !this.client) return;
    if (!(await this.dialNeedsAdmin(s))) return;
    if (!(await this.gainTarget(s))) return;
    this.clearDialNotice(id);
    const turn = this.turnFor(id);
    turn.cancel?.();
    turn.cancel = undefined;
    turn.targetDb = 0;
    await this.paintGainDial(s);
    await this.flushGain(id);
  }

  /** A push of the gain dial: mute or unmute the track. Mute applies on a matrix mix too. */
  async pushTrackGain(id: string): Promise<void> {
    await this.oneTap(id, () => this.flipDialMute(id));
  }

  private async flipDialMute(id: string): Promise<void> {
    const s = this.surfaces.get(id);
    if (!s || !this.client) return;
    if (!(await this.dialNeedsAdmin(s))) return;
    const target = await this.freshTarget(s);
    if (!target) return;
    this.clearDialNotice(id);
    const result = await this.sendLevel(s, target.dest, target.level.track, { muted: !target.level.muted });
    if (result) await this.noticeApplied(s, result);
    else await this.paintGainDial(s);
  }

  /**
   * Sends the level the turns have reached. One request at a time: if one is out, its return sends the
   * latest level, once. A refusal or a failure drops the turn, and the strip goes back to the stored level.
   */
  private async flushGain(id: string): Promise<void> {
    const s = this.surfaces.get(id);
    const turn = this.turns.get(id);
    if (!s || !turn || turn.targetDb === undefined || turn.inFlight) return;
    const target = this.destLevels ? this.trackTarget(s, this.destLevels) : undefined;
    if (!target || "face" in target) {
      turn.targetDb = undefined;
      await this.paintGainDial(s);
      return;
    }
    const db = turn.targetDb;
    turn.inFlight = true;
    const result = await this.sendLevel(s, target.dest, target.level.track, { gain: gainForDb(db) });
    turn.inFlight = false;
    if (!result) {
      turn.cancel?.();
      this.turns.delete(id);
      await this.paintGainDial(s);
      return;
    }
    if (turn.targetDb !== db) {
      // Turned again while this was out: send where it is now, after the same gathering window.
      turn.cancel ??= this.schedule(() => {
        turn.cancel = undefined;
        void this.flushGain(id);
      }, COALESCE_MS);
      return;
    }
    this.turns.delete(id);
    // A live change on every step would flash on every turn; a restart is shown, because the viewer saw it.
    if (result.applied === "restart") await this.noticeApplied(s, result);
    else await this.paintGainDial(s);
  }

  private async noticeApplied(s: Surface, result: TrackLevelResult): Promise<void> {
    const label = result.applied === "restart" ? "streamdeck.mute.appliedRestart" : "streamdeck.mute.appliedLive";
    await this.noticeOnDial(s, t(this.locale, label));
  }

  /**
   * Sends one track-level change. On success the cache takes the stored row the server answered with.
   * On a refusal the key keeps the server's words: 409 (audio copy) and 400 (a matrix mix, or a value
   * the server will not take) are "not available", 404 is a track outside the selection.
   */
  private async sendLevel(
    s: Surface,
    dest: DestinationLevels,
    track: number,
    change: { muted?: boolean; gain?: number },
  ): Promise<TrackLevelResult | undefined> {
    try {
      const result = await this.client!.setTrackLevel(dest.id, track, change, s.settings.sourceId ?? dest.sourceId);
      this.levelRefused.delete(s.id);
      if (result.destination && this.destLevels) {
        this.destLevels.set(result.destination.id, result.destination);
        this.levelsAt = ++this.levelsClock;
      }
      return result;
    } catch (err) {
      if (err instanceof ApiError && err.kind === "http" && (err.status === 409 || err.status === 400)) {
        this.levelRefused.set(s.id, { tone: "unavailable", label: "streamdeck.mute.unavailable", detail: err.message });
      } else if (err instanceof ApiError && err.kind === "http" && err.status === 404) {
        this.levelRefused.set(s.id, { tone: "unbound", label: "streamdeck.mute.noTrack", detail: err.message });
      }
      if (this.levelRefused.has(s.id)) await this.repaintTrackControl(s);
      await s.showAlert();
      return undefined;
    }
  }

  /**
   * Shows how a change reached the output for FLASH_MS. Live: the key's own face with "Applied live".
   * Restart: its own face and the server's reason, because the viewer saw a reconnect.
   */
  private async flashApplied(s: Surface, result: TrackLevelResult): Promise<void> {
    const face: KeyFace =
      result.applied === "restart"
        ? { tone: "restarted", label: "streamdeck.mute.appliedRestart", detail: result.reason }
        : { ...this.trackMuteFace(s), warning: "streamdeck.mute.appliedLive" };
    await s.showOk();
    await this.paintFace(s, face);
    this.flashes.get(s.id)?.();
    this.flashes.set(
      s.id,
      this.schedule(() => {
        this.flashes.delete(s.id);
        const current = this.surfaces.get(s.id);
        if (current) void this.paintFace(current, this.faceFor(current));
      }, FLASH_MS),
    );
  }

  /** Failover is handed back to the detector (auto). Forcing a source is not offered here. */
  async pressFailover(id: string): Promise<void> {
    const s = this.surfaces.get(id);
    if (!s || !this.client) return;
    if (!(await this.requireAdmin(s))) return;
    const active = this.statuses.get(scopeKey(s.settings.sourceId))?.failover?.active;
    if (active === undefined || active === "" || active === "primary") return;
    await this.run(s, () => this.client!.switchSource("auto", s.settings.sourceId));
  }

  // ---- helpers -----------------------------------------------------------

  /** Runs a tap's read-then-write unless one is already out for this key; a tap meanwhile is ignored. */
  private async oneTap(id: string, tap: () => Promise<void>): Promise<void> {
    if (this.tapping.has(id)) return;
    this.tapping.add(id);
    try {
      await tap();
    } finally {
      this.tapping.delete(id);
    }
  }

  /** Write keys need an admin token. The check is made BEFORE any request, so a read token never sends one. */
  private async requireAdmin(s: Surface): Promise<boolean> {
    if (this.isAdmin) return true;
    await s.setState(KEY_STATE.needsAdmin);
    await s.setTitle(t(this.locale, "streamdeck.auth.needsAdmin"));
    await s.showAlert();
    return false;
  }

  private async run(s: Surface, call: () => Promise<unknown>): Promise<void> {
    try {
      await call();
      await s.showOk();
    } catch {
      await s.showAlert();
    }
  }
}

/** The poll a key's settings describe, or undefined when they do not describe one yet. */
export function pollSpecOf(s: Surface): { question: string; options: string[]; durationSeconds: number } | undefined {
  const { pollQuestion, pollOptions, pollDuration } = s.settings;
  if (!pollQuestion || !pollOptions || pollOptions.length < 2 || !pollDuration) return undefined;
  return { question: pollQuestion, options: pollOptions, durationSeconds: pollDuration };
}

/** "1:05": the time left on a poll, from the server's end time. Never negative. */
export function pollClock(endsAt: string, now: number): string {
  const ends = Date.parse(endsAt);
  const left = Number.isNaN(ends) ? 0 : Math.max(0, Math.ceil((ends - now) / 1000));
  return `${Math.floor(left / 60)}:${String(left % 60).padStart(2, "0")}`;
}
