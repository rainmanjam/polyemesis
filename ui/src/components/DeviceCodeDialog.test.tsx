// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";

import { DeviceCodeDialog, UserCode } from "./DeviceCodeDialog";

const startDeviceAuth = vi.fn();
const pollDeviceAuth = vi.fn();
vi.mock("@/lib/api", () => ({
  api: {
    startDeviceAuth: (...a: unknown[]) => startDeviceAuth(...a),
    pollDeviceAuth: (...a: unknown[]) => pollDeviceAuth(...a),
  },
}));

/* This dialog exists for an operator on a box with no registrable callback --
 * often a headless machine reached over SSH. The whole feature reduces to one
 * thing: they have to be able to READ A CODE and type it somewhere else. */
describe("UserCode", () => {
  afterEach(cleanup);

  it("spells the code out for a screen reader", () => {
    render(<UserCode code="ABCD-1234" />);
    // "ABCD-1234" pronounced as a word gives the operator nothing to type.
    expect(screen.getByLabelText("A B C D - 1 2 3 4")).toBeTruthy();
  });

  it("shows the code verbatim and lets it be selected in one gesture", () => {
    const { container } = render(<UserCode code="WXYZ-9876" />);
    const el = container.querySelector("code")!;
    expect(el.textContent).toBe("WXYZ-9876");
    // select-all matters on a phone, where a drag-select over a spaced-out
    // monospace code is genuinely hard.
    expect(el.className).toContain("select-all");
  });
});

describe("DeviceCodeDialog", () => {
  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  const props = {
    platform: "twitch",
    platformName: "Twitch",
    open: true,
    onOpenChange: () => {},
    onConnected: () => {},
  };

  it("asks for a code as soon as it opens, and shows what came back", async () => {
    startDeviceAuth.mockResolvedValue({
      userCode: "ABCD-1234",
      verificationUri: "https://twitch.tv/activate",
      expiresAt: new Date(Date.now() + 600_000).toISOString(),
      intervalSeconds: 5,
      handle: "h1",
    });
    pollDeviceAuth.mockResolvedValue({ state: "pending" });

    render(<DeviceCodeDialog {...props} />);
    await waitFor(() => expect(screen.getByText("ABCD-1234")).toBeTruthy());
    expect(startDeviceAuth).toHaveBeenCalledWith("twitch");
    // The address is a real link: this operator is reading it on one machine and
    // opening it on another.
    const link = screen.getByRole("link") as HTMLAnchorElement;
    expect(link.href).toContain("twitch.tv/activate");
  });

  it("asks for nothing while closed", () => {
    render(<DeviceCodeDialog {...props} open={false} />);
    expect(startDeviceAuth).not.toHaveBeenCalled();
  });

  it("says so when the start fails, rather than showing an empty box", async () => {
    startDeviceAuth.mockRejectedValue(new Error("twitch is down"));
    render(<DeviceCodeDialog {...props} />);
    await waitFor(() =>
      expect(screen.getByText(/twitch is down/)).toBeTruthy(),
    );
  });
});
