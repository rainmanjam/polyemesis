import { describe, expect, it } from "vitest";

import { failoverNotice } from "@/lib/failoverNotice";
import type { FailoverSettings } from "@/lib/types";

const off: FailoverSettings = { enabled: false };
const on: FailoverSettings = { enabled: true };
const withFile: FailoverSettings = {
  enabled: true,
  playlist: { enabled: true, items: [{ upload: "standby.mkv" }] as never },
};

describe("failoverNotice", () => {
  it("warns once there is a broadcast to lose and nothing holding it up", () => {
    expect(failoverNotice(1, off)).toEqual({ kind: "unprotected" });
    expect(failoverNotice(9, off)).toEqual({ kind: "unprotected" });
  });

  /* The trigger is "there is something to lose", not "the stream is live". A
   * warning that waits for the broadcast arrives after the moment it was cheap
   * to act on. */
  it("says nothing on an install with nothing configured to go out", () => {
    expect(failoverNotice(0, off)).toEqual({ kind: "none" });
  });

  /* UNKNOWN IS NOT DISABLED. The settings read can fail or simply not have
   * landed, and telling an operator their broadcast is unprotected because a
   * request was slow is a false alarm on the most alarming thing this product
   * has to say. */
  it("says nothing when the settings have not been read", () => {
    expect(failoverNotice(3, null)).toEqual({ kind: "none" });
    expect(failoverNotice(3, undefined)).toEqual({ kind: "none" });
  });

  it("points at the empty playlist once failover itself is on", () => {
    // The machinery to loop an operator's own file is fully built; leaving it
    // empty means the fallback is a black slate.
    expect(failoverNotice(2, on)).toEqual({ kind: "slate-only" });
    expect(failoverNotice(2, { enabled: true, playlist: { enabled: false, items: [] } }))
      .toEqual({ kind: "slate-only" });
    expect(failoverNotice(2, { enabled: true, playlist: { enabled: true, items: [] } }))
      .toEqual({ kind: "slate-only" });
  });

  /* THE CONTROL CASE. Every branch above returns a notice, so a function that
   * always warned would pass all of them. A fully configured install must go
   * quiet, or the line becomes one an operator learns to ignore -- which is the
   * failure mode this notice was designed around in the first place. */
  it("goes quiet on an install that is actually protected", () => {
    expect(failoverNotice(4, withFile)).toEqual({ kind: "none" });
  });
});
