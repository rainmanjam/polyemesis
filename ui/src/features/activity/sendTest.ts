import { api } from "@/lib/api";
import type { Translator } from "@/lib/i18n";
import type { ActivityKind, ActivityTestResponse } from "@/lib/types";

/** A test alert's outcome, in the operator's words. */
export interface TestOutcome {
  ok: boolean;
  text: string;
}

/** The sentence for an answer from POST /activity/test. A refusal carries the
 *  server's own detail after ours, because it says what to change. */
export function describeTestResult(t: Translator, r: ActivityTestResponse): TestOutcome {
  if (r.accepted) return { ok: true, text: t("activity.test.accepted") };
  let head: string;
  if (r.reason === "disabled") head = t("activity.test.disabled");
  else if (r.reason === "duplicate") head = t("activity.test.duplicate");
  else head = t("activity.test.rejected");
  return { ok: false, text: r.detail ? `${head} ${r.detail}` : head };
}

/** Sends a test alert and never throws: a 429 (sent faster than twice a
 *  second) or a 400 carries the server's sentence, which is shown verbatim. */
export async function sendTestAlert(
  t: Translator,
  kind: ActivityKind,
  platform?: string,
): Promise<TestOutcome> {
  try {
    return describeTestResult(t, await api.sendTestActivity({ kind, ...(platform ? { platform } : {}) }));
  } catch (err) {
    return { ok: false, text: err instanceof Error && err.message ? err.message : t("activity.test.failed") };
  }
}
