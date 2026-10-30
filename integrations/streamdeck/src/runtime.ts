import { ApiError, PolyemesisClient, type Transport } from "./api.js";
import { ConfigError, keySettings, parseServerUrl, requireToken, type GlobalSettings, type KeySettings } from "./config.js";
import { t } from "./i18n.js";
import { destinationFace, failoverFace, type KeyFace } from "./keyFace.js";
import { HoldToConfirm, type Scheduler } from "./holdToConfirm.js";
import { meterFace } from "./levelsFeed.js";
import type { ConnectionInfo, LevelsReading, StatusPayload, StopImpact } from "./model.js";
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

export type SurfaceKind = "destination" | "goLive" | "stopAll" | "failover" | "meter";

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

  /** The stop key's state while a hold is loading or armed. The periodic repaint must not overwrite it. */
  private holdInProgress(id: string): boolean {
    const phase = this.holds.get(id)?.current;
    return phase === "loading" || phase === "arming";
  }

  add(surface: Surface): void {
    this.surfaces.set(surface.id, surface);
    if (surface.kind === "stopAll") {
      this.holds.set(
        surface.id,
        new HoldToConfirm(HOLD_MS, this.schedule, () => void this.confirmStop(surface)),
      );
    }
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
      await this.paintAll();
    } finally {
      this.statusBusy = false;
    }
  }

  private async refreshStatuses(): Promise<void> {
    const scopes = new Set<string>();
    for (const s of this.surfaces.values()) if (s.kind !== "meter") scopes.add(scopeKey(s.settings.sourceId));
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
      if (s.kind === "stopAll" && this.holdInProgress(s.id)) continue;
      const key = scopeKey(s.settings.sourceId);
      const scopeErr = this.scopeErrors.get(key);
      const payload = this.statuses.get(key);
      if (!this.client) {
        await this.paintFace(s, { tone: "idle", label: "streamdeck.auth.notPaired" });
      } else if (this.connectionError || !this.connection) {
        await this.paintFace(s, this.unavailableFace());
      } else if (scopeErr || !payload) {
        await this.paintFace(s, this.unavailableFace(scopeErr));
      } else if (s.kind === "destination") {
        const dest = payload.destinations?.find((d) => d.id === s.settings.destinationId);
        const face: KeyFace =
          s.settings.destinationId === undefined
            ? { tone: "unbound", label: "streamdeck.state.missing" }
            : destinationFace(dest, s.settings.sourceId, payload.destinationHold);
        await this.paintFace(s, face);
      } else if (s.kind === "failover") {
        await this.paintFace(s, failoverFace(payload.failover));
      } else if (s.kind === "goLive") {
        await this.paintFace(s, this.adminGated("streamdeck.go.live"));
      } else {
        await this.paintFace(s, this.adminGated("streamdeck.stop.hold"));
      }
    }
  }

  private adminGated(label: "streamdeck.go.live" | "streamdeck.stop.hold"): KeyFace {
    return this.isAdmin
      ? { tone: "idle", label }
      : { tone: "warn", label: "streamdeck.auth.needsAdmin" };
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

  /** Key down on a stop key. Loads the count fresh and arms the hold. */
  async pressStopDown(id: string): Promise<void> {
    const s = this.surfaces.get(id);
    const hold = this.holds.get(id);
    if (!s || !hold || !this.client) return;
    if (!(await this.requireAdmin(s))) return;
    const result = await hold.press(() => this.client!.stopImpact(s.settings.sourceId));
    if (result.phase === "refused") {
      await s.setTitle(stopTitle(undefined, this.locale));
      await s.showAlert();
    } else if (result.phase === "arming") {
      await s.setState(KEY_STATE.held);
      await s.setTitle(stopTitle(result.impact?.youtubeBroadcasts, this.locale));
    }
  }

  /** Key up, or the key leaving the screen: cancels any pending stop and restores the idle face. */
  async pressStopUp(id: string): Promise<void> {
    const s = this.surfaces.get(id);
    this.holds.get(id)?.release();
    if (!s) return;
    await s.setState(KEY_STATE.idle);
    await s.setTitle(t(this.locale, "streamdeck.stop.hold"));
  }

  private async confirmStop(s: Surface): Promise<void> {
    await this.run(s, () => this.client!.stopAll(s.settings.sourceId));
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
