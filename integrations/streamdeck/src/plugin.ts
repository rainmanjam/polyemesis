import streamDeck, {
  action,
  SingletonAction,
  type Action,
  type DidReceiveGlobalSettingsEvent,
  type DialDownEvent,
  type DialRotateEvent,
  type DidReceiveSettingsEvent,
  type KeyDownEvent,
  type KeyUpEvent,
  type SendToPluginEvent,
  type TouchTapEvent,
  type WillAppearEvent,
  type WillDisappearEvent,
} from "@elgato/streamdeck";
import type { JsonObject, JsonValue } from "@elgato/utils";
import { ApiError, PolyemesisClient } from "./api.js";
import { ConfigError, keySettings, parseServerUrl, type GlobalSettings, type KeySettings } from "./config.js";
import { localeFor } from "./i18n.js";
import type { StreamDeckKey } from "./keys.js";
import { Runtime, authKeyFor, clientFor, destinationChoices, type DestinationChoice, type Surface, type SurfaceKind } from "./runtime.js";
import { nodeTransport } from "./transport.js";

// The Stream Deck adapter. Each action forwards its events to the Runtime,
// which holds every rule. This file has no logic worth testing on its own; the
// tests cover what it calls.

const PLUGIN = "com.polyemesis.streamdeck";
/** The actions whose inspector picks a destination and one of its tracks. */
const TRACK_ACTIONS = [`${PLUGIN}.track-mute`, `${PLUGIN}.track-gain`];
// Status refreshes every couple of seconds; dials poll /levels five times a second.
// The server answers levels with its own freshness verdict, so the dial needs no
// faster clock than this to show NO FEED within about a window of silence.
const REFRESH_MS = 2000;
const METER_MS = 200;

let runtime: Runtime | undefined;
/** Created lazily: streamDeck.info is only populated once the plugin has connected. */
function rt(): Runtime {
  runtime ??= new Runtime(localeFor(streamDeck.info?.application?.language));
  return runtime;
}

function text(v: unknown): string | undefined {
  return typeof v === "string" ? v : undefined;
}

/** The saved pairing, kept for requests the inspector makes without sending its own form values. */
let savedGlobals: GlobalSettings = {};

/** Applies the global pairing settings, then repaints from a fresh check. */
function applyGlobals(raw: Record<string, unknown>): void {
  const globals: GlobalSettings = { url: text(raw.url), token: text(raw.token), caPem: text(raw.caPem) };
  savedGlobals = globals;
  rt().configure(globals);
  void rt().tick();
  // A preset picker open while the pairing is saved listed its presets against the OLD pairing
  // (or none). Send it a fresh list now, so pairing first and picking second works without a reopen.
  if (streamDeck.ui.action?.manifestId === `${PLUGIN}.preset`) {
    void presetList().then((list) => streamDeck.ui.sendToPropertyInspector(list));
  }
  // The same for a track picker, which lists the destinations of the programme the key is bound to.
  const open = streamDeck.ui.action;
  if (open && TRACK_ACTIONS.includes(open.manifestId)) {
    void open
      .getSettings()
      .then((settings) => destinationList(keySettings(settings as Record<string, unknown>).sourceId))
      .then((list) => streamDeck.ui.sendToPropertyInspector(list));
  }
}

/** Wraps an SDK action in the runtime's Surface shape. Keys and dials differ in which calls they accept. */
function surfaceFor(a: Action<JsonObject>, kind: SurfaceKind, settings: KeySettings): Surface {
  return {
    id: a.id,
    kind,
    settings,
    async setState(state) {
      if (a.isKey()) await a.setState(state);
    },
    async setTitle(title) {
      if (a.isKey()) await a.setTitle(title);
    },
    async showAlert() {
      if (a.isKey() || a.isDial()) await a.showAlert();
    },
    async showOk() {
      if (a.isKey()) await a.showOk();
    },
    async setFeedback(payload) {
      if (a.isDial()) await a.setFeedback(payload);
    },
  };
}

function register(a: Action<JsonObject>, kind: SurfaceKind, raw: Record<string, unknown>): void {
  rt().remove(a.id);
  rt().add(surfaceFor(a, kind, keySettings(raw)));
}

/**
 * The lifecycle every polyemesis action shares: register its surface when it appears or
 * its settings change, drop it when it leaves. Each action declares only its kind and
 * the presses it handles.
 */
abstract class PolyAction extends SingletonAction {
  abstract readonly kind: SurfaceKind;

  override onWillAppear(ev: WillAppearEvent): void {
    register(ev.action, this.kind, ev.payload.settings);
  }

  override onDidReceiveSettings(ev: DidReceiveSettingsEvent): void {
    register(ev.action, this.kind, ev.payload.settings);
  }

  override onWillDisappear(ev: WillDisappearEvent): void {
    rt().remove(ev.action.id);
  }
}

@action({ UUID: `${PLUGIN}.destination` })
export class DestinationKey extends PolyAction {
  readonly kind = "destination" as const;

  override onKeyDown(ev: KeyDownEvent): Promise<void> {
    return rt().pressDestination(ev.action.id);
  }
}

@action({ UUID: `${PLUGIN}.go-live` })
export class GoLiveKey extends PolyAction {
  readonly kind = "goLive" as const;

  override onKeyDown(ev: KeyDownEvent): Promise<void> {
    return rt().pressGoLive(ev.action.id);
  }
}

@action({ UUID: `${PLUGIN}.stop-all` })
export class StopAllKey extends PolyAction {
  readonly kind = "stopAll" as const;

  override onKeyDown(ev: KeyDownEvent): Promise<void> {
    return rt().pressStopDown(ev.action.id);
  }

  override onKeyUp(ev: KeyUpEvent): Promise<void> {
    return rt().pressHoldUp(ev.action.id);
  }
}

@action({ UUID: `${PLUGIN}.stop-destination` })
export class StopDestinationKey extends PolyAction {
  readonly kind = "stopOne" as const;

  override onKeyDown(ev: KeyDownEvent): Promise<void> {
    return rt().pressStopOneDown(ev.action.id);
  }

  override onKeyUp(ev: KeyUpEvent): Promise<void> {
    return rt().pressHoldUp(ev.action.id);
  }
}

@action({ UUID: `${PLUGIN}.record` })
export class RecordKey extends PolyAction {
  readonly kind = "record" as const;

  override onKeyDown(ev: KeyDownEvent): Promise<void> {
    return rt().pressRecord(ev.action.id);
  }
}

@action({ UUID: `${PLUGIN}.preset` })
export class PresetKey extends PolyAction {
  readonly kind = "preset" as const;

  override onKeyDown(ev: KeyDownEvent): Promise<void> {
    return rt().pressPresetDown(ev.action.id);
  }

  override onKeyUp(ev: KeyUpEvent): Promise<void> {
    return rt().pressHoldUp(ev.action.id);
  }
}

@action({ UUID: `${PLUGIN}.failover` })
export class FailoverKey extends PolyAction {
  readonly kind = "failover" as const;

  override onKeyDown(ev: KeyDownEvent): Promise<void> {
    return rt().pressFailover(ev.action.id);
  }
}

@action({ UUID: `${PLUGIN}.track-mute` })
export class TrackMuteKey extends PolyAction {
  readonly kind = "trackMute" as const;

  override onKeyDown(ev: KeyDownEvent): Promise<void> {
    return rt().pressTrackMute(ev.action.id);
  }
}

@action({ UUID: `${PLUGIN}.track-gain` })
export class TrackGainDial extends PolyAction {
  readonly kind = "trackGain" as const;

  override onDialRotate(ev: DialRotateEvent): Promise<void> {
    return rt().rotateTrackGain(ev.action.id, ev.payload.ticks);
  }

  override onDialDown(ev: DialDownEvent): Promise<void> {
    return rt().pushTrackGain(ev.action.id);
  }

  override onTouchTap(ev: TouchTapEvent): Promise<void> {
    return rt().touchTrackGain(ev.action.id);
  }
}

@action({ UUID: `${PLUGIN}.meter` })
export class MeterDial extends PolyAction {
  readonly kind = "meter" as const;
}

/** A connection check on the values the inspector is showing, not the saved ones. */
async function testConnection(g: GlobalSettings) {
  try {
    const client: PolyemesisClient = clientFor(g, nodeTransport);
    const info = await client.connection();
    return { ok: true as const, name: info.name, scope: info.scope, adminScope: info.adminScope };
  } catch (err) {
    if (err instanceof ConfigError) return { ok: false as const, key: err.key as StreamDeckKey, detail: undefined };
    if (err instanceof ApiError) return { ok: false as const, key: authKeyFor(err), detail: err.message };
    return { ok: false as const, key: "streamdeck.auth.unreachable" as StreamDeckKey, detail: String(err) };
  }
}

streamDeck.actions.registerAction(new DestinationKey());
streamDeck.actions.registerAction(new GoLiveKey());
streamDeck.actions.registerAction(new StopAllKey());
streamDeck.actions.registerAction(new StopDestinationKey());
streamDeck.actions.registerAction(new RecordKey());
streamDeck.actions.registerAction(new PresetKey());
streamDeck.actions.registerAction(new FailoverKey());
streamDeck.actions.registerAction(new TrackMuteKey());
streamDeck.actions.registerAction(new TrackGainDial());
streamDeck.actions.registerAction(new MeterDial());

streamDeck.settings.onDidReceiveGlobalSettings((ev: DidReceiveGlobalSettingsEvent<JsonObject>) => {
  applyGlobals(ev.settings as Record<string, unknown>);
});

streamDeck.ui.onSendToPlugin(async (ev: SendToPluginEvent<JsonValue, JsonObject>) => {
  // The inspector's labels come from a catalog it ships itself (ui/strings.js), so
  // the only message it sends is a connection check on the values it is showing.
  const msg = ev.payload as { type?: unknown; url?: unknown; token?: unknown; caPem?: unknown; sourceId?: unknown };
  if (msg.type === "test") {
    const result = await testConnection({ url: text(msg.url), token: text(msg.token), caPem: text(msg.caPem) });
    await streamDeck.ui.sendToPropertyInspector({ type: "test", ...result });
  } else if (msg.type === "presets") {
    // The preset picker reads the saved pairing, never the inspector's form: a token typed but not saved
    // must not be what lists the presets.
    await streamDeck.ui.sendToPropertyInspector(await presetList());
  } else if (msg.type === "destinations") {
    // The same rule as the presets: the saved pairing lists them, never the inspector's form.
    await streamDeck.ui.sendToPropertyInspector(await destinationList(keySettings({ sourceId: msg.sourceId }).sourceId));
  }
});

/** The destinations and tracks for a track picker, or the key that says why they could not be listed. */
async function destinationList(
  sourceId: number | undefined,
): Promise<{ type: "destinations"; destinations?: DestinationChoice[]; key?: StreamDeckKey }> {
  try {
    const client = clientFor(savedGlobals, nodeTransport);
    return { type: "destinations", destinations: await destinationChoices(client, sourceId, rt().language) };
  } catch (err) {
    if (err instanceof ConfigError) return { type: "destinations", key: err.key as StreamDeckKey };
    if (err instanceof ApiError) return { type: "destinations", key: authKeyFor(err) };
    return { type: "destinations", key: "streamdeck.pi.destinationsFailed" as StreamDeckKey };
  }
}

/** The routing presets for the inspector's picker, or the key that says why they could not be listed. */
async function presetList(): Promise<{ type: "presets"; presets?: { id: string; name: string }[]; key?: StreamDeckKey }> {
  try {
    const client = clientFor(savedGlobals, nodeTransport);
    return { type: "presets", presets: await client.routingPresets() };
  } catch (err) {
    if (err instanceof ConfigError) return { type: "presets", key: err.key as StreamDeckKey };
    if (err instanceof ApiError) return { type: "presets", key: authKeyFor(err) };
    return { type: "presets", key: "streamdeck.pi.presetsFailed" as StreamDeckKey };
  }
}

await streamDeck.connect();

const globals = (await streamDeck.settings.getGlobalSettings()) as Record<string, unknown>;
applyGlobals(globals);
setInterval(() => void rt().tick(), REFRESH_MS);
setInterval(() => void rt().tickMeters(), METER_MS);
