/* The console's side of the server's contract.
 *
 * wire.generated.ts is written by the Go test TestWireContract (internal/api)
 * from the server's own constants, so a state word, an event type or a bound
 * cannot be added, renamed or moved on the server without this file changing
 * with it. Everything here is DERIVED from that file; nothing in lib restates
 * one of its values. */

import { WIRE } from "./wire.generated";

export { WIRE };

type Member<T extends readonly string[]> = T[number];

/** internal/failreason.Cause. A cause newer than this build still arrives as a
 *  string: see causeLabel in failureRemedy.ts. */
export type FailureCause = Member<typeof WIRE.failureCauses>;
/** internal/supervisor.State. */
export type ProcessState = Member<typeof WIRE.processStates>;
/** internal/jobs.State. */
export type JobState = Member<typeof WIRE.jobStates>;
/** internal/db token scopes. */
export type TokenScope = Member<typeof WIRE.tokenScopes>;
/** internal/chat.State. */
export type ChatState = Member<typeof WIRE.chatStates>;
/** internal/obsbridge.ConnState. */
export type ObsConnState = Member<typeof WIRE.obsConnStates>;
/** internal/db offload item states. */
export type OffloadState = Member<typeof WIRE.offloadStates>;
/** internal/events.Type: every event the WebSocket and /events carry. */
export type EventType = Member<typeof WIRE.eventTypes>;

/** The ports every install publishes (db.DefaultSettings). Read this where a
 *  settings response lacks `listeners`: a server older than the field. */
export const DEFAULT_LISTENERS = {
  srtPort: WIRE.defaults.srtPort as number,
  rtmpPort: WIRE.defaults.rtmpPort as number,
};

type PlatformRow = (typeof WIRE.platforms.platforms)[number];

/** The value a destination's `platform` column carries (internal/platform
 *  Row.AcceptsDestinations). "trovo" is retired: nothing offers it, and it
 *  stays because rows saved before the shutdown still carry it. "rumble" is
 *  here because the registry lets a destination carry it, though its preset
 *  saves as custom. */
export type Platform = Extract<PlatformRow, { destination: true }>["enum"];

/** The platforms a chat message can come from: the ones the server has a chat
 *  adapter for (internal/chat). Not the same set as Platform: Vimeo has no chat
 *  adapter, and Rumble has chat but no stored-destination preset. */
export type ChatPlatform = Extract<PlatformRow, { chat: true }>["enum"];
