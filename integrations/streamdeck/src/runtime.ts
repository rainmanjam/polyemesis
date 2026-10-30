import { ApiError, PolyemesisClient, type Transport } from "./api.js";
import { ConfigError, keySettings, parseServerUrl, requireToken, type GlobalSettings, type KeySettings } from "./config.js";
import { t } from "./i18n.js";
import type { StreamDeckKey } from "./keys.js";
import { destinationFace, failoverFace, type KeyFace } from "./keyFace.js";
import { HoldToConfirm, type Scheduler } from "./holdToConfirm.js";
import { LevelsFeed, meterFace } from "./levelsFeed.js";
import type { ConnectionInfo, LevelsFrame, StatusPayload, StopImpact } from "./model.js";
import { KEY_STATE, stateFor, stopTitle, titleFor } from "./render.js";
import { nodeTransport } from "./transport.js";

// The plugin's brain. It owns pairing, the status cache, admin gating and what
// each key does on press. It knows nothing about the Stream Deck SDK: a
// Surface is the one thing the SDK adapter provides per key, and the tests
// supply fakes. That split is what lets every rule here run under vitest.

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
export function authKeyFor(err: ApiError): StreamDeckKey {
  if (err.kind === "unauthorized") return "streamdeck.auth.refused";
  return "streamdeck.auth.unreachable";
}

/** Builds the client for a pairing. Throws ConfigError when the pairing is incomplete. */
export function clientFor(globals: GlobalSettings, makeTransport: (ca?: string) => Transport): PolyemesisClient {
  const base = parseServerUrl(globals.url);
  const token = requireToken(globals.token);
  return new PolyemesisClient(base, token, makeTransport(globals.caPem));
}

export class Runtime {
  private globals: GlobalSettings = {};
  private client: PolyemesisClient | undefined;
  private pairingError: ConfigError | undefined;
  private connection: ConnectionInfo | undefined;
  private connectionError: ApiError | undefined;
  private readonly surfaces = new Map<string, Surface>();
  private readonly holds = new Map<string, HoldToConfirm>();
  private readonly statuses = new Map<string, StatusPayload>();
  private readonly feed = new LevelsFeed(100);

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
    this.client = undefined;
    try {
      this.client = clientFor(globals, this.makeTransport);
    } catch (err) {
      this.pairingError = err instanceof ConfigError ? err : undefined;
      if (!this.pairingError) throw err;
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

  /** The meters interval the server pushes at, so the freshness window matches it. */
  async refreshMeterInterval(): Promise<void> {
    if (!this.client) return;
    try {
      this.feed.setInterval(await this.client.meterIntervalMs());
    } catch {
      // Keep the last known interval. A dial falls back to NO FEED on its own.
    }
  }

  ingestFrame(frame: LevelsFrame): void {
    this.feed.onFrame(frame);
  }

  // ---- surfaces ----------------------------------------------------------

  add(surface: Surface): void {
    this.surfaces.set(surface.id, surface);
    if (surface.kind === "stopAll") {
      this.holds.set(
        surface.id,
        new HoldToConfirm(
          HOLD_MS,
          this.schedule,
          () => void this.confirmStop(surface),
        ),
      );
    }
  }

  remove(id: string): void {
    this.holds.get(id)?.release();
    this.holds.delete(id);
    this.surfaces.delete(id);
  }

  /** Repaints every surface from the latest cache. Call on a timer and after any press. */
  async tick(): Promise<void> {
    if (!this.client) {
      await this.paintAll(undefined);
      return;
    }
    if (!this.connection && !this.connectionError) {
      await this.verify().catch(() => undefined);
    }
    if (this.connectionError) {
      await this.paintAll(undefined);
      return;
    }
    await this.refreshStatuses();
    await this.paintAll(this.statuses);
  }

  private async refreshStatuses(): Promise<void> {
    const scopes = new Set<string>();
    for (const s of this.surfaces.values()) scopes.add(scopeKey(s.settings.sourceId));
    for (const key of scopes) {
      try {
        const sourceId = key === ALL_SCOPE ? undefined : Number(key);
        this.statuses.set(key, (await this.client!.status(sourceId)) as StatusPayload);
      } catch (err) {
        if (err instanceof ApiError && (err.kind === "unauthorized" || err.kind === "untrusted")) {
          this.connectionError = err;
        }
      }
    }
  }

  // ---- painting ----------------------------------------------------------

  private async paintAll(statuses: Map<string, StatusPayload> | undefined): Promise<void> {
    for (const s of this.surfaces.values()) {
      if (s.kind === "meter") {
        await this.paintMeter(s);
        continue;
      }
      if (!statuses) {
        await this.paintFace(s, this.unavailableFace());
        continue;
      }
      const payload = statuses.get(scopeKey(s.settings.sourceId));
      if (s.kind === "destination") {
        const dest = payload?.destinations?.find((d) => d.id === s.settings.destinationId);
        const face: KeyFace =
          s.settings.destinationId === undefined
            ? { tone: "unbound", label: "streamdeck.state.missing" }
            : destinationFace(dest, s.settings.sourceId, payload?.destinationHold);
        await this.paintFace(s, face);
      } else if (s.kind === "failover") {
        await this.paintFace(s, failoverFace(payload?.failover));
      } else if (s.kind === "goLive") {
        await this.paintFace(s, { tone: this.isAdmin ? "idle" : "warn", label: this.isAdmin ? "streamdeck.go.live" : "streamdeck.auth.needsAdmin" });
      } else {
        await this.paintFace(s, { tone: this.isAdmin ? "idle" : "warn", label: this.isAdmin ? "streamdeck.stop.hold" : "streamdeck.auth.needsAdmin" });
      }
    }
  }

  private unavailableFace(): KeyFace {
    if (!this.client) return { tone: "idle", label: "streamdeck.auth.notPaired" };
    const err = this.connectionError;
    return { tone: "error", label: err ? authKeyFor(err) : "streamdeck.auth.unreachable", detail: err?.message };
  }

  private async paintFace(s: Surface, face: KeyFace): Promise<void> {
    await s.setState(stateFor(face.tone));
    await s.setTitle(titleFor(face, this.locale));
  }

  private async paintMeter(s: Surface): Promise<void> {
    const face = meterFace(this.feed.state(), this.feed.current());
    const value = face.peakDb !== undefined ? `${face.peakDb.toFixed(0)} dB` : t(this.locale, face.label ?? "streamdeck.meter.noFeed");
    await s.setFeedback({ title: "", value, indicator: Math.round(face.value * 100) });
  }

  // ---- presses -----------------------------------------------------------

  /** A destination key starts its destination when it is off. Stopping is the stop key's job. */
  async pressDestination(id: string): Promise<void> {
    const s = this.surfaces.get(id);
    if (!s || !this.client || s.settings.destinationId === undefined) return;
    if (!(await this.requireAdmin(s))) return;
    const dest = this.destinationOf(s);
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
    const active = (this.statuses.get(scopeKey(s.settings.sourceId))?.failover as { active?: string } | undefined)?.active;
    if (active === undefined || active === "" || active === "primary") return;
    await this.run(s, () => this.client!.switchSource("auto", s.settings.sourceId));
  }

  // ---- helpers -----------------------------------------------------------

  private destinationOf(s: Surface): { enabled: boolean } | undefined {
    const payload = this.statuses.get(scopeKey(s.settings.sourceId));
    return payload?.destinations?.find((d) => d.id === s.settings.destinationId);
  }

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

/** A hold is 1.5 s: long enough that a tap cannot trigger a stop by accident. */
export const HOLD_MS = 1500;

const ALL_SCOPE = "*";
function scopeKey(sourceId: number | undefined): string {
  return sourceId === undefined ? ALL_SCOPE : String(sourceId);
}
