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

  /* A SLATE IS NOT AN EXPOSURE. This used to warn when failover was on but no
   * playlist file was configured, on the grounds that the fallback would be a
   * black slate rather than the operator's own video. Photographing the demo
   * install showed why that was wrong: a permanent, un-dismissible line across
   * the dashboard of a correctly configured install.
   *
   * The test is whether the broadcast survives, and with a slate it does --
   * the connection is held and nothing unrecoverable happens. What viewers see
   * during the gap is a preference, and making a standing warning out of a
   * preference is how this line becomes one an operator scrolls past, on the
   * day it says the thing that matters. */
  it("says nothing once failover is on, slate or not", () => {
    expect(failoverNotice(2, on)).toEqual({ kind: "none" });
    expect(failoverNotice(2, { enabled: true, playlist: { enabled: false, items: [] } }))
      .toEqual({ kind: "none" });
  });

  /* THE CONTROL CASE. A function that always warned would pass the first test,
   * so a protected install has to be shown to go quiet -- otherwise the line
   * becomes one an operator learns to ignore, which is the failure mode this
   * notice was designed around in the first place. */
  it("goes quiet on an install that is actually protected", () => {
    expect(failoverNotice(4, withFile)).toEqual({ kind: "none" });
  });
});
