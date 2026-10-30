import { describe, expect, it } from "vitest";
import { remedyFor, processLogHref } from "./failureRemedy";
import { translate } from "./i18n";
import type { FailureCause } from "./types";

const ALL: FailureCause[] = [
  "key_rejected", "platform_not_ready", "dns", "connection_refused", "timeout",
  "tls", "codec_rejected", "upload_too_slow", "unknown",
];

/* A9: every failure reason comes with something to DO. A reason with no remedy
 * is a diagnosis an operator has to translate into an action at the worst
 * moment; the table below is that translation, done once. */
describe("remedyFor", () => {
  it("has an English remedy sentence and an action for every cause", () => {
    for (const c of ALL) {
      const r = remedyFor(c);
      const text = translate("en", r.text);
      expect(text, c).not.toBe(r.text);
      expect(text.length, c).toBeGreaterThan(10);
      expect(["edit", "renditions", "log"]).toContain(r.action);
    }
  });

  it("sends the causes that clear by themselves to the log, not to an edit", () => {
    // Editing a destination that is only waiting on the network rebuilds it
    // and throws away the retry it was already making.
    for (const c of ["platform_not_ready", "timeout"] as const) {
      expect(remedyFor(c).action).toBe("log");
    }
  });

  it("sends a refused key, a bad address and a TLS mismatch to the destination's settings", () => {
    for (const c of ["key_rejected", "dns", "connection_refused", "tls"] as const) {
      expect(remedyFor(c).action).toBe("edit");
    }
  });

  it("sends a refused codec and a slow upload to the renditions page", () => {
    expect(remedyFor("codec_rejected").action).toBe("renditions");
    expect(remedyFor("upload_too_slow").action).toBe("renditions");
  });

  it("falls back to the log for a cause newer than this UI", () => {
    expect(remedyFor("something_new" as FailureCause).action).toBe("log");
  });
});

describe("processLogHref", () => {
  it("deep-links the Monitoring log, filtered to one process", () => {
    expect(processLogHref("dest:3")).toBe("/monitoring?process=dest%3A3#process-log");
    expect(processLogHref("ingest", 2)).toBe("/monitoring?programme=2&process=ingest#process-log");
    expect(processLogHref("ingest", null)).toBe("/monitoring?process=ingest#process-log");
  });
});
