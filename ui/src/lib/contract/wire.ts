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
