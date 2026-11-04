// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

import { OffloadBadge } from "./OffloadStatus";
import { setLanguage } from "@/lib/i18n";
import type { RecordingOffload } from "@/lib/types";

/* A Library row's upload badge reads one as one. It said "Upload failed on 1
 * target(s)" before the catalogue gave count 1 its own key.
 *
 * summarise() calls a row "stuck" when any target is stuck, otherwise "failed"
 * when any failed; the number is failed + stuck either way. */

const on = (targetId: number, state: RecordingOffload["targets"][number]["state"]) => ({
  targetId,
  targetName: `target-${targetId}`,
  state,
  attempts: 1,
});

afterEach(() => {
  cleanup();
  setLanguage("en");
});

describe("OffloadBadge: failed uploads", () => {
  it("says Upload failed on 1 target", () => {
    render(<OffloadBadge recordingId={1} offload={{ targets: [on(1, "failed"), on(2, "verified")] }} />);
    expect(screen.getByTestId("offload-badge").dataset.state).toBe("failed");
    expect(screen.getByText("Upload failed on 1 target")).toBeTruthy();
    expect(screen.queryByText("Upload failed on 1 targets")).toBeNull();
    expect(screen.queryByText("Upload failed on 1 target(s)")).toBeNull();
  });

  it("says Upload failed on 2 targets", () => {
    render(<OffloadBadge recordingId={1} offload={{ targets: [on(1, "failed"), on(2, "failed")] }} />);
    expect(screen.getByTestId("offload-badge").dataset.state).toBe("failed");
    expect(screen.getByText("Upload failed on 2 targets")).toBeTruthy();
  });
});

describe("OffloadBadge: stuck uploads", () => {
  it("says Upload stuck on 1 target", () => {
    render(<OffloadBadge recordingId={1} offload={{ targets: [on(1, "stuck"), on(2, "verified")] }} />);
    expect(screen.getByTestId("offload-badge").dataset.state).toBe("stuck");
    expect(screen.getByText("Upload stuck on 1 target")).toBeTruthy();
    expect(screen.queryByText("Upload stuck on 1 targets")).toBeNull();
    expect(screen.queryByText("Upload stuck on 1 target(s)")).toBeNull();
  });

  it("says Upload stuck on 2 targets", () => {
    render(<OffloadBadge recordingId={1} offload={{ targets: [on(1, "stuck"), on(2, "stuck")] }} />);
    expect(screen.getByTestId("offload-badge").dataset.state).toBe("stuck");
    expect(screen.getByText("Upload stuck on 2 targets")).toBeTruthy();
  });
});
