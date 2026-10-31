import defaults from "./contract/job-policy-defaults.json";
import type { PostProdSettings } from "./types";

/* WHAT THE JOBS POLICY TAB MAY FOLD AWAY, AND WHEN (A63).
 *
 * The tab leads with four decisions -- default mode, jobs at once, CPU ceiling,
 * CPU resume -- and puts the other twelve settings behind an Advanced
 * disclosure. A disclosure that hides a changed value is a trap: the operator
 * who set the battery floor to 0 a year ago would see a page that looks like
 * the factory policy and behaves like something else. So the rule is that the
 * disclosure is closed only while every setting inside it holds its default
 * and is in range, and that takes the TRUE default of each field.
 *
 * THE DEFAULTS ARE THE SERVER'S. contract/job-policy-defaults.json mirrors
 * DefaultSettings().PostProd in internal/db/settings.go (which takes most of
 * its numbers from the Default* constants in internal/jobs/governor.go), and
 * TestTheUIsJobPolicyDefaultsAreTheServersDefaults in internal/db fails the
 * moment the two disagree. No endpoint reports them, which is why a file.
 *
 * "0 MEANS DEFAULT" IS TRUE OF THREE FIELDS AND NO OTHERS. normalise() in
 * internal/jobs/governor.go substitutes the default for a zero sustain, settle
 * and defer window, so a stored 0 there behaves exactly as the default and is
 * not a change. Everywhere else 0 is a real setting: ingest linger 0 releases
 * the stream gate at once, the battery floor and thermal ceiling 0 switch the
 * gate off, a nice level of 0 is full priority, and 0 retention days keeps the
 * history forever. Treating those as "default" would hide exactly the changes
 * an operator most needs to see. */

export const JOB_POLICY_DEFAULTS: Readonly<
  Omit<PostProdSettings, "kinds" | "whisperModel" | "defaultMode"> & { defaultMode: string }
> = defaults;

/** A number field in the Advanced section: its key and the range the input
 *  accepts. The ranges are the inputs' own, as they were before the redesign. */
export interface AdvancedNumberField {
  key:
    | "cpuSustainedSeconds"
    | "cpuSettleSeconds"
    | "ingestLingerSeconds"
    | "deferSeconds"
    | "niceLevel"
    | "batteryFloorPercent"
    | "thermalCeilingC"
    | "retainDays"
    | "retainJobs";
  min: number;
  max: number;
}

export const ADVANCED_NUMBER_FIELDS: readonly AdvancedNumberField[] = [
  { key: "cpuSustainedSeconds", min: 0, max: 3600 },
  { key: "cpuSettleSeconds", min: 0, max: 3600 },
  { key: "ingestLingerSeconds", min: 0, max: 3600 },
  { key: "deferSeconds", min: 0, max: 3600 },
  { key: "niceLevel", min: 0, max: 19 },
  { key: "batteryFloorPercent", min: 0, max: 100 },
  { key: "thermalCeilingC", min: 0, max: 120 },
  { key: "retainDays", min: 0, max: 3650 },
  { key: "retainJobs", min: 0, max: 100000 },
];

export type AdvancedSwitchKey = "idleIo" | "avoidGpuWhenStreaming" | "gpuBusy";

export const ADVANCED_SWITCH_FIELDS: readonly AdvancedSwitchKey[] = [
  "idleIo",
  "avoidGpuWhenStreaming",
  "gpuBusy",
];

/** The three windows whose zero the governor replaces with its default. */
const ZERO_IS_DEFAULT: ReadonlySet<AdvancedNumberField["key"]> = new Set([
  "cpuSustainedSeconds",
  "cpuSettleSeconds",
  "deferSeconds",
]);

export function rangeOf(key: AdvancedNumberField["key"]): AdvancedNumberField {
  return ADVANCED_NUMBER_FIELDS.find((f) => f.key === key)!;
}

/** Whether a number field holds something other than its default. */
export function numberChanged(key: AdvancedNumberField["key"], value: number): boolean {
  if (value === JOB_POLICY_DEFAULTS[key]) return false;
  return !(value === 0 && ZERO_IS_DEFAULT.has(key));
}

/** Whether a number field is outside the range its input accepts. A value the
 *  server stored outside it, or one typed and not yet committed, is a value
 *  the operator has to see to fix. */
export function numberInvalid(key: AdvancedNumberField["key"], value: number): boolean {
  const { min, max } = rangeOf(key);
  return !Number.isFinite(value) || value < min || value > max;
}

export function switchChanged(key: AdvancedSwitchKey, value: boolean): boolean {
  return value !== JOB_POLICY_DEFAULTS[key];
}

/** How many Advanced settings must not be folded away: changed from their
 *  default, or out of range. Zero means the disclosure may stay closed. */
export function advancedNeedingAttention(draft: PostProdSettings): number {
  let n = 0;
  for (const { key } of ADVANCED_NUMBER_FIELDS) {
    const v = draft[key];
    if (numberChanged(key, v) || numberInvalid(key, v)) n++;
  }
  for (const key of ADVANCED_SWITCH_FIELDS) {
    if (switchChanged(key, draft[key])) n++;
  }
  return n;
}

/** The count the disclosure's title names. */
export const ADVANCED_SETTING_COUNT = ADVANCED_NUMBER_FIELDS.length + ADVANCED_SWITCH_FIELDS.length;
