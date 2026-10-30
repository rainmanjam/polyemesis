import { ApiError, PolyemesisClient, type Transport } from "./api.js";
import type { StreamDeckKey } from "./keys.js";
import { ConfigError, keySettings, parseServerUrl, requireToken, type GlobalSettings, type KeySettings } from "./config.js";
import { t } from "./i18n.js";
import { destinationFace, failoverFace, type KeyFace } from "./keyFace.js";
import { HoldToConfirm, type Scheduler } from "./holdToConfirm.js";
import { meterFace } from "./levelsFeed.js";
import type { ConnectionInfo, LevelsReading, RecordingState, StatusPayload, StopImpact } from "./model.js";
import { KEY_STATE, stateFor, stopTitle, titleFor } from "./render.js";
import { nodeTransport } from "./transport.js";

// The plugin's brain. It owns pairing, the status cache, admin gating and what
// each key does on press. It knows nothing about the Stream Deck SDK: a
// Surface is the one thing the SDK adapter provides per key, and the tests
// supply fakes. That split is what lets every rule here run under vitest.
//
// Two clocks drive it. tick() runs every couple of seconds and refreshes the
// status cache and the faces built from it. tickMeters() runs several times a
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
  | "meter";

/** Kinds that confirm by a held press. A stop ends broadcasts for good; a preset can restart destinations. */
const HELD_KINDS: readonly SurfaceKind[] = ["stopAll", "stopOne", "preset"];

/** What an armed hold will act on, captured at key down. A later settings edit cannot retarget it. */
interface Armed {
  destinationId?: number;
  presetId?: string;
  sourceId?: number;
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

const ALL_SCOPE = "*";
function scopeKey(sourceId: number | undefined): string {
  return sourceId === undefined ? ALL_SCOPE : String(sourceId);
}

/** A hold is 1.5 s: long enough that a tap cannot trigger a stop by accident. */
export const HOLD_MS = 1500;

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
  private readonly statuses = new Map<string, StatusPayload>();
  private readonly levels = new Map<string, LevelsReading | { error: true }>();
  private readonly lastMeterPaint = new Map<string, string>();
  /** The install-wide recording state, read each tick while a Record key is on the deck. */
  private recordingState: RecordingState | undefined;
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
    this.presetNames = undefined;
    this.presetRefused.clear();
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
      if (this.hasKind("preset")) await this.loadPresetNames();
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
      if (s.kind !== "meter" && s.kind !== "record" && s.kind !== "preset") scopes.add(scopeKey(s.settings.sourceId));
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
      await this.paintFace(s, this.faceFor(s));
    }
  }

  /** What a key shows right now, from the cached answers. The one place a key face is decided. */
  private faceFor(s: Surface): KeyFace {
    if (!this.client) return { tone: "idle", label: "streamdeck.auth.notPaired" };
    if (this.connectionError || !this.connection) return this.unavailableFace();
    if (s.kind === "record") return this.recordFace();
    if (s.kind === "preset") return this.presetFace(s);
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
        return destinationFace(dest, s.settings.sourceId, payload.destinationHold);
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

  /** The preset key: unbound, no ingest after a refused apply, needs admin, or ready to hold. */
  private presetFace(s: Surface): KeyFace {
    const presetId = s.settings.presetId;
    if (presetId === undefined) return { tone: "unbound", label: "streamdeck.state.missing" };
    if (this.presetRefused.has(s.id)) return { tone: "error", label: "streamdeck.preset.noIngest" };
    const face = this.adminGated("streamdeck.preset.hold");
    return face.tone === "idle" ? { ...face, detail: this.presetNames?.get(presetId) ?? presetId } : face;
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
    if (result.phase === "arming") {
      await s.setState(KEY_STATE.held);
      await s.setTitle(
        titleFor(
          { tone: "held", label: "streamdeck.preset.applying", detail: this.presetNames?.get(presetId) ?? presetId },
          this.locale,
        ),
      );
    }
  }

  /** Key down on the record key. Reads the switch fresh, then flips it. Tap, not hold: see README. */
  async pressRecord(id: string): Promise<void> {
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
