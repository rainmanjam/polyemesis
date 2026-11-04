/* Clip publishing (Wave 4 #5c): the wire types and the pure helpers.

   Nothing here posts anything. The console's Publish button is the only caller
   of POST /publish, and the server refuses it without a confirmation token that
   POST /publish/prepare minted for exactly the form on screen. */

export type PublishPlatformId = "youtube" | "tiktok" | "x";

/** Why a publication failed. Mirrors internal/publish's Cause; new causes are
 *  added at the end and an unknown one is rendered as "unknown". */
export type PublishCause =
  | "needs_reconnect"
  | "token_rejected"
  | "quota_exceeded"
  | "rate_limited"
  | "platform_unavailable"
  | "network"
  | "clip_missing"
  | "clip_rejected"
  | "privacy_unavailable"
  | "platform_refused"
  | "outcome_unknown"
  | "gated"
  | "not_confirmed"
  | "cancelled"
  | "unknown"
  | "upload_limited";

export const PUBLISH_CAUSES: readonly PublishCause[] = [
  "needs_reconnect",
  "token_rejected",
  "quota_exceeded",
  "rate_limited",
  "platform_unavailable",
  "network",
  "clip_missing",
  "clip_rejected",
  "privacy_unavailable",
  "platform_refused",
  "outcome_unknown",
  "gated",
  "not_confirmed",
  "cancelled",
  "unknown",
  "upload_limited",
];

export interface PublishMeta {
  title: string;
  description: string;
  tags: string[];
  privacy: string;
}

export interface PublishAccount {
  id: number;
  name: string;
  needsReconnect?: boolean;
  connectPath?: string;
}

export interface PublishPlatform {
  id: PublishPlatformId;
  enabled: boolean;
  gatedReason?: string;
  configured: boolean;
  privacy: { default: string; options: string[]; forced: boolean };
  accounts: PublishAccount[];
  quotaUnits?: number;
}

export type PublicationState = "confirmed" | "uploading" | "posted" | "failed";

export interface PublicationJob {
  id: number;
  state: string;
  progress: number;
  attempts: number;
  blocked?: boolean;
  reason?: string;
  etaSeconds?: number;
}

export interface Publication {
  id: number;
  idempotencyKey: string;
  clipJobId: number;
  platform: PublishPlatformId;
  accountId: number;
  accountName: string;
  title: string;
  description: string;
  tags: string[];
  privacy: string;
  state: PublicationState;
  platformPostId?: string;
  postUrl?: string;
  actualPrivacy?: string;
  failCause?: string;
  failDetail?: string;
  attempts: number;
  createdAt: string;
  updatedAt: string;
  postedAt?: string;
  reason?: { cause: PublishCause; line?: string };
  job?: PublicationJob;
}

export interface PublishTargets {
  clipJobId: number;
  platforms: PublishPlatform[];
  prefill: PublishMeta;
  seconds: number;
  vertical: boolean;
  publications: Publication[];
}

export interface PublishPrepared {
  confirmToken: string;
  expiresAt: string;
  platform: PublishPlatformId;
  accountId: number;
  accountName: string;
  meta: PublishMeta;
  privacyForced: boolean;
  quotaUnits?: number;
  existing?: Publication;
}

export interface PublishForm {
  clipJobId: number;
  platform: PublishPlatformId;
  accountId: number;
  title: string;
  description: string;
  tags: string[];
  privacy: string;
  /** The person looked at the platform after an outcome_unknown failure and the
   *  post is not there. The server refuses to re-arm that failure without it. */
  acknowledgeOutcome?: boolean;
}

/** "fun, #clips gaming" becomes ["fun","clips","gaming"]. The # is optional on
 *  input and added back wherever a platform shows hashtags. */
export function parseTags(text: string): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const raw of text.split(/[\s,]+/)) {
    const tag = raw.replace(/^#+/, "").trim();
    if (!tag || seen.has(tag.toLowerCase())) continue;
    seen.add(tag.toLowerCase());
    out.push(tag);
  }
  return out;
}

export function tagsText(tags: readonly string[]): string {
  return tags.map((t) => `#${t}`).join(" ");
}

/** True once the form has what the server needs to check it. */
export function formComplete(f: Partial<PublishForm>): f is PublishForm {
  return Boolean(f.clipJobId && f.platform && f.accountId && (f.title ?? "").trim());
}

/** The identity of a form, so a confirmation is only ever used for the form it
 *  was minted for. Field order is fixed. */
export function formKey(f: PublishForm): string {
  return JSON.stringify([f.clipJobId, f.platform, f.accountId, f.title.trim(), f.description.trim(), f.tags, f.privacy]);
}

export function isPublishCause(c: string | undefined): c is PublishCause {
  return !!c && (PUBLISH_CAUSES as readonly string[]).includes(c);
}

export function inFlight(p: Publication | null | undefined): boolean {
  return !!p && (p.state === "confirmed" || p.state === "uploading");
}
