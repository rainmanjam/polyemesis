import { describe, expect, it } from "vitest";

import { ApiError } from "./api";
import { MAX_POLL_FAILURES, pollVerdict } from "./metadataPush";

/* The composer's rule for giving a push up. A 404 is the server saying the job
 * is gone -- nothing a retry can fix; anything else is retried, but not for
 * ever. The rule is the whole difference between "status lost" and a composer
 * stuck on "Pushing…" until the tab is reloaded. */
describe("pollVerdict", () => {
  it("gives up at once on a 404", () => {
    expect(pollVerdict(new ApiError(404, "no such metadata push"), 1)).toBe("lost");
  });

  it("retries a transient failure", () => {
    expect(pollVerdict(new ApiError(502, "request failed (502)"), 1)).toBe("retry");
    expect(pollVerdict(new TypeError("Failed to fetch"), 1)).toBe("retry");
  });

  it("gives up after MAX_POLL_FAILURES in a row", () => {
    expect(pollVerdict(new ApiError(502, "x"), MAX_POLL_FAILURES - 1)).toBe("retry");
    expect(pollVerdict(new ApiError(502, "x"), MAX_POLL_FAILURES)).toBe("lost");
  });
});
