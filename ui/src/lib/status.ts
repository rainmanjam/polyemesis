import { stateLabelKey, type TranslationKey } from "./i18n";
import type { OutputState } from "./outputStrip";
import type { IngestPhase } from "./sourceIngest";
import type { ChatSanction, DelayStatus, ProcessState, ProcessStatus } from "./types";

/* ONE VOCABULARY FOR EVERY "IS IT RUNNING" WORD ON THE CONSOLE.
 *
 * Before this, the Dashboard said STOPPED on a destination, Clips said OFF,
 * the ingest chip said Offline and Recordings printed the raw recorder state
 * in lower case. Each page had its own spelling of "not running", and an
 * operator who learned one could not read the next.
 *
 * This module is PRESENTATION ONLY. It reads API state values and returns a
 * translation key, a tone and a non-colour indicator. It never renames a state
 * value, and nothing sent back to the server is spelled with these words. The
 * API, MQTT, webhook and Stream Deck vocabularies are published contracts.
 *
 * Tones follow the signal rule in docs/DESIGN-SYSTEM.md: green (ok) only for
 * something that is on air, amber (warn) for a transition or something that
 * needs a look, red (bad) for a failure, and idle for everything not running
 * by choice or by absence. A failed item is never drawn like a disabled one.
 *
 * Every domain returns a StatusReading, so a page renders one shape. Labels
 * come from the existing state.* catalogue through stateLabelKey, so
 * ProcessState is spelled in one place and not twice. */

export type StatusTone = "ok" | "warn" | "bad" | "idle";

/** The non-colour cue. It travels with the tone so a reading survives a
 *  greyscale screen, a colour-blind operator, or a text-only log line. */
export type StatusIndicator = "filled" | "dashed" | "cross" | "ring";

export interface StatusReading {
  labelKey: TranslationKey;
  tone: StatusTone;
  indicator: StatusIndicator;
  /** On air or recording: StatusPill draws an ok reading that carries this as
   *  the solid --tally lamp. Every other ok reading (a process running, a
   *  signal present) is the nominal case and is drawn quietly. Absent, never
   *  false, so readings without it compare equal to what they always were. */
  lamp?: true;
}

const INDICATOR: Record<StatusTone, StatusIndicator> = {
  ok: "filled",
  warn: "dashed",
  bad: "cross",
  idle: "ring",
};

function reading(labelKey: TranslationKey, tone: StatusTone): StatusReading {
  return { labelKey, tone, indicator: INDICATOR[tone] };
}

/** Marks an on-air or recording reading as a lamp. Only an ok reading lights:
 *  a failed destination is a failure, not a lamp. */
function lamp(r: StatusReading): StatusReading {
  return r.tone === "ok" ? { ...r, lamp: true } : r;
}

const PROCESS_TONE: Record<ProcessState, StatusTone> = {
  running: "ok",
  starting: "warn",
  reconnecting: "warn",
  failed: "bad",
  stopped: "idle",
};

/** A child process (ingest, encoder, output, recorder). Undefined or null is
 *  the absence of a process, which reads as Stopped, the same as stateLabelKey. */
export function processStatus(state?: ProcessState | null): StatusReading {
  return reading(stateLabelKey(state), state ? PROCESS_TONE[state] : "idle");
}

/** The state a destination's process is IN, for anything that draws it as on
 *  air or not: the server's state, except that "running" with no media yet is
 *  "starting".
 *
 *  RUNNING IS NOT ON AIR UNTIL MEDIA MOVES. The supervisor reports "running"
 *  from the moment FFmpeg is spawned (supervisor.go sets StateRunning straight
 *  after cmd.Start) -- before it has connected, let alone published -- and
 *  leaves uptimeSec at 0 until this run's output has carried media. It also
 *  keeps the previous run's failure reason until a run moves media. So a Kick
 *  destination that Amazon IVS refused on every connect drew a red LIVE lamp
 *  at 0 kbps with "--:--:--" uptime, beside the failure box, while each
 *  respawn was in flight. The server's state is right about the process; it
 *  is this reading of it as "on air" that was wrong, so it is corrected here
 *  and nothing sent back to the server changes. */
export function destinationProcessState(
  process?: Pick<ProcessStatus, "state" | "uptimeSec"> | null,
): ProcessState | undefined {
  const state = process?.state;
  if (state === "running" && !(process!.uptimeSec > 0)) return "starting";
  return state ?? undefined;
}

/** A destination. Disabled means the operator switched it off, so the process
 *  state underneath is not what the card should say: it reads Off, never
 *  Stopped, so a switched-off card is not read as a broadcast that ended
 *  (app-council A2). The card, the Renditions chip and the on-air counts all
 *  take their word from here (A91). */
export function destinationStatus(
  enabled: boolean,
  state?: ProcessState | null,
  delayPhase?: DelayStatus["phase"] | null,
): StatusReading {
  if (!enabled) return reading("state.off", "idle");
  // A broadcast delay fills before any process exists, by design (#11). With no
  // process yet, that is a filling state, not a stopped one.
  if (!state && delayPhase === "buffering") return reading("wave3.bugs.delayFilling", "warn");
  // A running destination is ON AIR, not merely running: the tally lamp.
  return lamp(processStatus(state));
}

/** A broadcast-delay buffer phase (types.ts DelayStatus). Buffering is filling
 *  and nothing has aired, so it reads as starting. Draining is still airing the
 *  tail after a stop, which is on air but no longer live. */
export function delayStatus(phase: "buffering" | "on-air" | "draining"): StatusReading {
  switch (phase) {
    case "on-air":
      return lamp(reading("state.live", "ok"));
    case "draining":
      return reading("state.draining", "warn");
    default:
      return reading("state.starting", "warn");
  }
}

/** A playlist or rendition item. Ready is not live, so it is idle, not green. */
export function renditionStatus(state: "ready" | "transcoding" | "attention"): StatusReading {
  switch (state) {
    case "ready":
      return reading("state.ready", "idle");
    case "transcoding":
      return reading("state.transcoding", "warn");
    default:
      return reading("state.attention", "warn");
  }
}

/** The recorder. A null recorder means recording is switched off, which is an
 *  operator choice and reads as Off, not as Offline or Failed. */
export function recorderStatus(recorder?: ProcessStatus | null): StatusReading {
  // A running recorder is recording: the REC lamp.
  return recorder ? lamp(processStatus(recorder.state)) : reading("state.off", "idle");
}

/** The clip replay buffer (ClipStatus in the API). Running means it is holding
 *  footage ready to clip. Switched on but not running is waiting for a stream,
 *  which is a transition and reads as a warning; it must not read as Ready
 *  while the capture buttons it would enable are still disabled. */
export function clipBufferStatus(buffer: { enabled: boolean; running: boolean }): StatusReading {
  if (buffer.running) return reading("state.ready", "idle");
  if (buffer.enabled) return reading("state.waiting", "warn");
  return reading("state.off", "idle");
}

/** A source's ingest (sourceIngest.ts). Receiving is the only green. */
export function ingestPhaseStatus(phase: IngestPhase): StatusReading {
  switch (phase) {
    case "receiving":
      return reading("state.live", "ok");
    case "waiting":
      return reading("state.waiting", "idle");
    case "unavailable":
      return reading("state.unavailable", "bad");
    case "off":
      return reading("state.off", "idle");
    default:
      return reading("state.notSet", "idle");
  }
}

/** A loudness monitor on an output (outputStrip.ts). Stale means the last
 *  report is old, so the number is not to be trusted, which is a warning. */
export function outputStatus(state: OutputState): StatusReading {
  switch (state.kind) {
    case "live":
      return reading("state.live", "ok");
    case "starting":
      return reading("state.starting", "warn");
    case "stale":
      return reading("state.stale", "warn");
    case "error":
      return reading("state.failed", "bad");
    case "absent":
      return reading("state.stopped", "idle");
    default:
      return reading("state.off", "idle");
  }
}

/** What a platform row on Settings → Platforms says about it
 *  (settings-council R1, R10). One word per row, in this order of precedence:
 *  an account that has to reconnect outranks "connected", because a token that
 *  lost a permission stops working mid-show; a pair the platform refused
 *  outranks "saved", because "saved" over a refused pair is what an operator
 *  re-reads for an hour. "Credentials saved" is a warning, not a pass: nothing
 *  can be streamed through it until an account is connected. */
export function platformSetupStatus(input: {
  supported: boolean;
  hasSecret: boolean;
  lastCheckState?: "verified" | "unverified" | "rejected" | "unreachable";
  accounts: number;
  reconnectNeeded: boolean;
}): StatusReading {
  if (input.reconnectNeeded) return reading("settingsUx.pillReconnect", "warn");
  if (input.accounts > 0) return reading("settingsUx.pillConnected", "ok");
  if (!input.supported) return reading("settingsUx.pillManualKey", "idle");
  if (input.hasSecret && input.lastCheckState === "rejected") return reading("settingsUx.pillCredsRejected", "bad");
  if (input.hasSecret) return reading("settingsUx.pillCredsSaved", "warn");
  return reading("settingsUx.pillNotSetUp", "idle");
}

/** A viewer's standing on the chat user card (UX Council A48). A ban and a
 *  timeout are both amber: each is a moderation decision in force that the
 *  moderator may want to revisit, and neither is a failure, so neither is red.
 *  An ABSENT sanction is the server saying it could not tell, which reads as
 *  Unknown and is never drawn like "no active ban". */
export function chatSanctionStatus(sanction?: ChatSanction | null): StatusReading {
  switch (sanction?.action) {
    case "ban":
      return reading("chatCard.sanctionBanned", "warn");
    case "timeout":
      return reading("chatCard.sanctionTimedOut", "warn");
    case "none":
      return reading("chatCard.sanctionNone", "idle");
    default:
      return reading("chatCard.sanctionUnknown", "idle");
  }
}
