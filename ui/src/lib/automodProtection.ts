import type { TranslationKey } from "@/lib/i18n";
import type {
  AutomodAction,
  AutomodCeiling,
  AutomodProtection,
  AutomodProtectionCategory,
  AutomodSettings,
} from "@/lib/types";

/* The recommended-protection pack, as the console draws it.
 *
 * The pack's own data (its terms, its tiers) lives in the server binary and is
 * never copied here. What IS here is the small vocabulary the card needs to
 * lay itself out: which categories there are, which detectors sit in each, and
 * the pack's defaults for them. internal/api has a test that reads this file
 * and fails when it names a detector the server does not have, or the server
 * has one this file lacks, so the two cannot drift apart silently.
 *
 * Everything the operator changes is written as a DIFF over those defaults
 * (see AutomodProtection in types.ts): a switch that matches the pack's default
 * is removed from the draft rather than stored, so a release that updates the
 * pack still reaches an install that never customised it. */

export type FalsePositiveRisk = "rare" | "sometimes" | "often";

export interface ProtectionDetectorDef {
  /** The name after the slash: the key under `detectors`. */
  id: string;
  /** The full group, which `holdDetectors` is keyed by. */
  group: string;
  label: TranslationKey;
  /** Whether the detector runs when the operator has not touched it. */
  defaultOn: boolean;
  /** Whether a hit also keeps the message off the overlays by default. */
  defaultHold: boolean;
  /** False for the history checker's own detectors: they run whatever this
   *  card says, so only their hold policy can be changed here. */
  switchable: boolean;
}

export interface ProtectionCategoryDef {
  id: string;
  label: TranslationKey;
  risk: FalsePositiveRisk;
  /** Whether the category runs when the operator has not touched it. */
  defaultOn: boolean;
  /** The highest rung the pack lets this category climb by default. */
  ceiling: AutomodCeiling;
  detectors: readonly ProtectionDetectorDef[];
  /** A category whose terms the operator can add to (hate, profanity). */
  terms?: readonly string[];
}

const det = (
  category: string,
  id: string,
  label: TranslationKey,
  defaultHold: boolean,
  opts: { defaultOn?: boolean; switchable?: boolean; group?: string } = {},
): ProtectionDetectorDef => ({
  id,
  group: opts.group ?? `${category}/${id}`,
  label,
  defaultOn: opts.defaultOn ?? true,
  defaultHold,
  switchable: opts.switchable ?? true,
});

export const PROTECTION_CATEGORIES: readonly ProtectionCategoryDef[] = [
  {
    id: "hate_speech",
    label: "automod.protCatHate",
    risk: "rare",
    defaultOn: true,
    ceiling: "ban",
    terms: ["severe", "contextual"],
    detectors: [
      det("hate_speech", "severe", "automod.protDetSevere", true),
      det("hate_speech", "contextual", "automod.protDetContextual", false),
    ],
  },
  {
    id: "doxxing",
    label: "automod.protCatDoxxing",
    risk: "sometimes",
    defaultOn: true,
    ceiling: "timeout",
    detectors: [
      det("doxxing", "phone", "automod.protDetPhone", true),
      det("doxxing", "email", "automod.protDetEmail", true),
      det("doxxing", "ipv4", "automod.protDetIpv4", true),
      det("doxxing", "ipv6", "automod.protDetIpv6", true),
      det("doxxing", "address", "automod.protDetAddress", true, { defaultOn: false }),
    ],
  },
  {
    id: "scam",
    label: "automod.protCatScam",
    risk: "sometimes",
    defaultOn: true,
    ceiling: "ban",
    detectors: [
      det("scam", "giveaway", "automod.protDetGiveaway", true),
      det("scam", "bio", "automod.protDetBio", true),
      det("scam", "support", "automod.protDetSupport", true),
      det("scam", "shortener", "automod.protDetShortener", true),
      det("scam", "lookalike", "automod.protDetLookalike", true),
    ],
  },
  {
    id: "flood",
    label: "automod.protCatFlood",
    risk: "often",
    defaultOn: true,
    ceiling: "timeout",
    detectors: [
      det("flood", "zalgo", "automod.protDetZalgo", true),
      det("flood", "char", "automod.protDetChar", true),
      det("flood", "emote", "automod.protDetEmote", false),
      det("flood", "mentions", "automod.protDetMentions", true),
      det("flood", "links", "automod.protDetLinks", true),
      det("flood", "caps", "automod.protDetCaps", false),
      det("flood", "near_duplicate", "automod.protDetNearDuplicate", false, {
        group: "spam_near_duplicate",
      }),
      // The history checker's own detectors: they run whatever this card says.
      det("flood", "repeats", "automod.protDetRepeats", false, { switchable: false, group: "repeats" }),
      det("flood", "message_rate", "automod.protDetMessageRate", false, {
        switchable: false,
        group: "message_rate",
      }),
    ],
  },
  {
    id: "impersonation",
    label: "automod.protCatImpersonation",
    risk: "sometimes",
    defaultOn: true,
    ceiling: "hold",
    detectors: [det("impersonation", "impersonation", "automod.protCatImpersonation", true, { group: "impersonation" })],
  },
  {
    id: "raid",
    label: "automod.protCatRaid",
    risk: "rare",
    defaultOn: true,
    ceiling: "delete",
    detectors: [det("raid", "raid", "automod.protCatRaid", true, { group: "raid" })],
  },
  {
    id: "profanity",
    label: "automod.protCatProfanity",
    risk: "often",
    defaultOn: false,
    ceiling: "delete",
    terms: ["mild", "strong"],
    detectors: [],
  },
];

/** The ladder's rungs, lowest first. Each is armed by the matrix cell
 *  `platform/rung/protection`. */
export const LADDER_RUNGS = ["delete", "timeout", "ban"] as const;
export type LadderRung = (typeof LADDER_RUNGS)[number];

/** Rungs polyemesis cannot take back, which ask first. */
export const IRREVERSIBLE_RUNGS: readonly LadderRung[] = ["delete", "ban"];

/** The ceilings a category may be set to. `self_harm` is not among them: it is
 *  hold-only and the server refuses anything higher. */
export const CEILINGS: readonly AutomodCeiling[] = ["hold", "delete", "timeout", "ban"];

export const RUNG_KEYS: Record<LadderRung, TranslationKey> = {
  delete: "automod.actionDelete",
  timeout: "automod.actionTimeout",
  ban: "automod.actionBan",
};

export const CEILING_KEYS: Record<AutomodCeiling, TranslationKey> = {
  hold: "automod.protCeilHold",
  delete: "automod.actionDelete",
  timeout: "automod.actionTimeout",
  ban: "automod.actionBan",
};

export const RISK_KEYS: Record<FalsePositiveRisk, TranslationKey> = {
  rare: "automod.protRiskRare",
  sometimes: "automod.protRiskSometimes",
  often: "automod.protRiskOften",
};

/** The default ladder timeout and strike window, in seconds. */
export const DEFAULT_LADDER_SECONDS = 600;

export function rungKey(platform: string, rung: LadderRung | AutomodAction): string {
  return `${platform}/${rung}/protection`;
}

/** The block as stored, or an undecided empty one for a document from a server
 *  that predates it. */
export function protectionOf(a: AutomodSettings | undefined): AutomodProtection {
  return (
    a?.protection ?? {
      state: "",
      pack: "en",
      packVersion: 0,
      impersonation: {},
      strikeWindowSeconds: DEFAULT_LADDER_SECONDS,
      ladderTimeoutSeconds: DEFAULT_LADDER_SECONDS,
    }
  );
}

export function categoryEnabled(p: AutomodProtection, def: ProtectionCategoryDef): boolean {
  return p.categories?.[def.id]?.enabled ?? def.defaultOn;
}

export function detectorEnabled(
  p: AutomodProtection,
  cat: ProtectionCategoryDef,
  d: ProtectionDetectorDef,
): boolean {
  if (!d.switchable) return true;
  return p.categories?.[cat.id]?.detectors?.[d.id] ?? d.defaultOn;
}

export function detectorHolds(p: AutomodProtection, d: ProtectionDetectorDef): boolean {
  return p.holdDetectors?.[d.group] ?? d.defaultHold;
}

/** Whether the category, as configured, keeps anything off the overlays. */
export function categoryHoldState(
  p: AutomodProtection,
  cat: ProtectionCategoryDef,
): "held" | "some" | "console" {
  if (cat.detectors.length === 0) return "held";
  const live = cat.detectors.filter((d) => detectorEnabled(p, cat, d));
  const holding = live.filter((d) => detectorHolds(p, d)).length;
  if (holding === 0) return "console";
  return holding === live.length ? "held" : "some";
}

export function ceilingOf(p: AutomodProtection, cat: ProtectionCategoryDef): AutomodCeiling {
  return p.ceilings?.[cat.id] ?? cat.ceiling;
}

/** Sparse writes: a value equal to the pack default is removed, not stored. */
function pruneCategory(c: AutomodProtectionCategory): AutomodProtectionCategory | undefined {
  const detectors = c.detectors && Object.keys(c.detectors).length > 0 ? c.detectors : undefined;
  if (c.enabled === undefined && !detectors) return undefined;
  return { ...(c.enabled === undefined ? {} : { enabled: c.enabled }), ...(detectors ? { detectors } : {}) };
}

export function withCategoryEnabled(
  p: AutomodProtection,
  def: ProtectionCategoryDef,
  on: boolean,
): AutomodProtection {
  const categories = { ...p.categories };
  const next = pruneCategory({
    ...categories[def.id],
    enabled: on === def.defaultOn ? undefined : on,
  });
  if (next) categories[def.id] = next;
  else delete categories[def.id];
  return { ...p, categories };
}

export function withDetectorEnabled(
  p: AutomodProtection,
  cat: ProtectionCategoryDef,
  d: ProtectionDetectorDef,
  on: boolean,
): AutomodProtection {
  const categories = { ...p.categories };
  const detectors = { ...categories[cat.id]?.detectors };
  if (on === d.defaultOn) delete detectors[d.id];
  else detectors[d.id] = on;
  const next = pruneCategory({ ...categories[cat.id], detectors });
  if (next) categories[cat.id] = next;
  else delete categories[cat.id];
  return { ...p, categories };
}

export function withDetectorHold(
  p: AutomodProtection,
  d: ProtectionDetectorDef,
  hold: boolean,
): AutomodProtection {
  const holdDetectors = { ...p.holdDetectors };
  if (hold === d.defaultHold) delete holdDetectors[d.group];
  else holdDetectors[d.group] = hold;
  return { ...p, holdDetectors };
}

export function withCeiling(
  p: AutomodProtection,
  cat: ProtectionCategoryDef,
  ceiling: AutomodCeiling,
): AutomodProtection {
  const ceilings = { ...p.ceilings };
  if (ceiling === cat.ceiling) delete ceilings[cat.id];
  else ceilings[cat.id] = ceiling;
  return { ...p, ceilings };
}

/** The ladder timeout as a short length, in minutes when it is a whole number
 *  of them. Returns the catalogue key and its parameter. */
export function durationParts(seconds: number): { key: TranslationKey; n: number } {
  if (seconds >= 60 && seconds % 60 === 0) return { key: "automod.protMinutes", n: seconds / 60 };
  return { key: "automod.protSeconds", n: seconds };
}

/** Which rungs are armed for a platform, lowest first. */
export function armedRungs(on: Record<string, boolean> | undefined, platform: string): LadderRung[] {
  return LADDER_RUNGS.filter((r) => on?.[rungKey(platform, r)] === true);
}

/** The catalogue key for a detector the server named (a pack group such as
 *  "doxxing/phone", or a history detector such as "repeats"). Undefined for one
 *  this console does not know, which the caller shows as the name it was given
 *  rather than inventing a label. */
export function detectorLabelKey(detector: string): TranslationKey | undefined {
  for (const c of PROTECTION_CATEGORIES) {
    for (const d of c.detectors) if (d.group === detector) return d.label;
  }
  return undefined;
}

/** The term tiers, in the operator's words. */
export const TIER_KEYS: Record<string, TranslationKey> = {
  severe: "automod.protTierSevere",
  contextual: "automod.protTierContextual",
  mild: "automod.protTierMild",
  strong: "automod.protTierStrong",
};
