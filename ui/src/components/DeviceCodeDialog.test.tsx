// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";

import { DeviceCodeDialog, UserCode } from "./DeviceCodeDialog";

const startDeviceAuth = vi.fn();
const pollDeviceAuth = vi.fn();
vi.mock("@/lib/api", () => ({
  api: {
    startDeviceAuth: (...a: unknown[]) => startDeviceAuth(...a),
    pollDeviceAuth: (...a: unknown[]) => pollDeviceAuth(...a),
  },
}));

/** The success toast is the only thing that says an account arrived, because
 *  the dialog closes itself the moment it does. */
const toastSuccess = vi.fn();
vi.mock("sonner", () => ({
  toast: { success: (...a: unknown[]) => toastSuccess(...a) },
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

/* ===========================================================================
   THE POLLING STATES, WHICH ARE THE WHOLE OF THIS DIALOG.

   Everything above is one render. What this dialog actually is, is a loop with
   five ways out -- pending, slow-down, expired, denied, connected -- and none
   of them is visible in a screenshot of the working case. Two of them cost the
   operator something real if they are wrong:

     polling faster than the platform asked rate-limits their whole developer
     app, mid-connect, and the only symptom is that connecting stopped working

     polling past the code's expiry leaves them watching a spinner that can
     never resolve, spending requests on a foregone conclusion

   Fake timers rather than waiting, because the interval under test is measured
   in tens of seconds and the assertion is about WHEN a request is made.
   =========================================================================== */

const NOW = "2026-08-19T12:00:00Z";
const AUTH = {
  handle: "h-1",
  userCode: "ABCD-1234",
  verificationUri: "https://www.twitch.tv/activate?public=true",
  /** Thirty minutes out from NOW: Twitch's own device code lifetime. */
  expiresAt: "2026-08-19T12:30:00Z",
  intervalSeconds: 5,
};

describe("DeviceCodeDialog while it waits for the operator", () => {
  const props = {
    platform: "twitch",
    platformName: "Twitch",
    open: true,
    onOpenChange: () => {},
    onConnected: () => {},
  };

  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date(NOW));
    startDeviceAuth.mockResolvedValue({ ...AUTH });
  });

  afterEach(() => {
    cleanup();
    vi.useRealTimers();
    vi.clearAllMocks();
  });

  /** Renders and lets the start request settle, which is what puts a code on
   *  screen and starts the loop. */
  const open = async (over: Partial<typeof props> = {}) => {
    const view = render(<DeviceCodeDialog {...props} {...over} />);
    await act(async () => {});
    return view;
  };

  /** Moves the clock, running whatever the loop scheduled along the way. */
  const advance = async (ms: number) => {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(ms);
    });
  };

  it("waits the platform's interval before it polls at all", async () => {
    pollDeviceAuth.mockResolvedValue({ state: "pending", retryInSeconds: 5 });
    await open();

    expect(screen.getByText("ABCD-1234")).toBeTruthy();
    // The code has only just appeared. An immediate poll is a request the
    // operator cannot possibly have answered yet.
    expect(pollDeviceAuth).not.toHaveBeenCalled();

    await advance(4_999);
    expect(pollDeviceAuth).not.toHaveBeenCalled();
    await advance(1);
    expect(pollDeviceAuth).toHaveBeenCalledTimes(1);
    // The handle from the start response, not the platform name: it is the
    // whole identity of this flow.
    expect(pollDeviceAuth).toHaveBeenCalledWith("twitch", "h-1");
  });

  // RFC 8628's slow_down, which is the platform telling the operator's app to
  // back off. Ignoring it is how one connect attempt rate-limits every feature
  // that shares those credentials.
  it("obeys an interval the platform widens mid-flow", async () => {
    pollDeviceAuth
      .mockResolvedValueOnce({ state: "pending", retryInSeconds: 5 })
      .mockResolvedValue({ state: "pending", retryInSeconds: 60 });
    await open();

    await advance(5_000);
    expect(pollDeviceAuth).toHaveBeenCalledTimes(1);
    await advance(5_000);
    expect(pollDeviceAuth).toHaveBeenCalledTimes(2);

    // That second answer said sixty. The next poll is a minute away, not five
    // seconds -- the start response's interval must not win here.
    await advance(59_000);
    expect(pollDeviceAuth).toHaveBeenCalledTimes(2);
    await advance(1_000);
    expect(pollDeviceAuth).toHaveBeenCalledTimes(3);
  });

  // The server repeats retryInSeconds on every pending answer, but it is not
  // required to. Falling back to the FLOOR instead of to the interval the flow
  // started with would quietly triple the request rate against a platform that
  // asked for thirty seconds.
  it("keeps the flow's own interval when an answer omits one", async () => {
    startDeviceAuth.mockResolvedValue({ ...AUTH, intervalSeconds: 30 });
    pollDeviceAuth.mockResolvedValue({ state: "pending" });
    await open();

    await advance(30_000);
    expect(pollDeviceAuth).toHaveBeenCalledTimes(1);
    await advance(29_999);
    expect(pollDeviceAuth).toHaveBeenCalledTimes(1);
    await advance(1);
    expect(pollDeviceAuth).toHaveBeenCalledTimes(2);
  });

  // Not decoration. The operator is typing on a phone and needs to know whether
  // they have time to finish -- and the dialog stops on this number.
  it("shows how long the code has, and counts it down", async () => {
    pollDeviceAuth.mockResolvedValue({ state: "pending" });
    await open();

    expect(screen.getByText("30:00")).toBeTruthy();
    await advance(61_000);
    expect(screen.getByText("28:59")).toBeTruthy();
  });

  it("stops on its own clock rather than polling a code it knows is dead", async () => {
    // Three seconds left and a five-second interval: by the time the first poll
    // comes due the code cannot possibly still be good.
    startDeviceAuth.mockResolvedValue({
      ...AUTH,
      expiresAt: "2026-08-19T12:00:03Z",
    });
    pollDeviceAuth.mockResolvedValue({ state: "pending" });
    await open();

    await advance(5_000);
    // An expired code can only ever answer "invalid device code". Asking is a
    // request spent against the operator's rate limit on a foregone conclusion.
    expect(pollDeviceAuth).not.toHaveBeenCalled();
    expect(
      screen.getByText("The code expired. Start again to get a new one."),
    ).toBeTruthy();

    // And it is over: no spinner that never resolves.
    await advance(120_000);
    expect(pollDeviceAuth).not.toHaveBeenCalled();
  });

  it("connects once, tells the caller, and closes itself", async () => {
    pollDeviceAuth.mockResolvedValue({
      state: "connected",
      account: { accountName: "Dallas" },
    });
    const onConnected = vi.fn();
    const onOpenChange = vi.fn();
    const { rerender } = await open({ onConnected, onOpenChange });

    await advance(5_000);
    expect(toastSuccess).toHaveBeenCalledWith("Connected Dallas.");
    expect(onConnected).toHaveBeenCalledTimes(1);
    // A "connected" screen the operator has to dismiss is a screen between them
    // and the thing they came here to do.
    expect(onOpenChange).toHaveBeenCalledWith(false);

    // It stopped polling the moment it settled.
    await advance(120_000);
    expect(pollDeviceAuth).toHaveBeenCalledTimes(1);

    // A re-render from the page above -- which passes fresh closures every time
    // it renders -- must not connect the account a second time. Two reloads of
    // the account list and two toasts for one connection.
    rerender(
      <DeviceCodeDialog
        {...props}
        onConnected={() => onConnected()}
        onOpenChange={(o) => onOpenChange(o)}
      />,
    );
    await act(async () => {});
    expect(onConnected).toHaveBeenCalledTimes(1);
    expect(toastSuccess).toHaveBeenCalledTimes(1);
  });

  it("names the platform when the account arrived without a display name", async () => {
    // The account IS connected; a blank display name is not a failure. What it
    // must not produce is "Connected ." with a hole where the name goes.
    pollDeviceAuth.mockResolvedValue({
      state: "connected",
      account: { accountName: "" },
    });
    await open();

    await advance(5_000);
    expect(toastSuccess).toHaveBeenCalledWith("Connected Twitch.");
  });

  // The server sends a sentence the operator can act on. Showing our own
  // instead would lose the half that says what to do -- and which platform.
  it("repeats the platform's refusal verbatim and offers another go", async () => {
    pollDeviceAuth.mockResolvedValue({
      state: "expired",
      reason: "this code has already been used or has expired; start again",
    });
    await open();

    await advance(5_000);
    expect(screen.getByText(/already been used/)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Try again" })).toBeTruthy();
    // Stopped, not retrying on its own.
    await advance(120_000);
    expect(pollDeviceAuth).toHaveBeenCalledTimes(1);
  });

  it("falls back to its own sentence when the refusal came with no words", async () => {
    pollDeviceAuth.mockResolvedValue({ state: "expired" });
    await open();

    await advance(5_000);
    // Never an empty warning line. This is also the case that must stay
    // translatable: a paraphrase invented in the reducer would win the fallback
    // and pin every operator to English.
    expect(
      screen.getByText("The code expired. Start again to get a new one."),
    ).toBeTruthy();
  });

  it("stops rather than spins when the server answers a word it cannot read", async () => {
    pollDeviceAuth.mockResolvedValue({ state: "revoked" });
    await open();

    await advance(5_000);
    expect(screen.getByText(/does not understand/)).toBeTruthy();
    await advance(120_000);
    expect(pollDeviceAuth).toHaveBeenCalledTimes(1);
  });

  // A transport failure is not the end of the flow. The code is still good and
  // the operator may still be typing.
  it("keeps waiting through a failed poll instead of tearing the flow down", async () => {
    pollDeviceAuth
      .mockRejectedValueOnce(new Error("network went away"))
      .mockResolvedValue({ state: "connected", account: { accountName: "Dallas" } });
    const onConnected = vi.fn();
    await open({ onConnected });

    await advance(5_000);
    expect(pollDeviceAuth).toHaveBeenCalledTimes(1);
    // The code is still on screen and nothing is being blamed on the operator.
    expect(screen.getByText("ABCD-1234")).toBeTruthy();
    expect(screen.queryByText(/expired/i)).toBeNull();

    await advance(5_000);
    expect(pollDeviceAuth).toHaveBeenCalledTimes(2);
    expect(onConnected).toHaveBeenCalledTimes(1);
  });

  // Every polling panel has this race and most find it in production: a request
  // already in flight when the operator gives up.
  it("does not connect an account behind an operator who closed the dialog", async () => {
    // The first flow's poll is still in flight when the operator gives up.
    let land: (res: unknown) => void = () => {};
    pollDeviceAuth.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          land = resolve;
        }),
    );
    pollDeviceAuth.mockResolvedValue({ state: "pending", retryInSeconds: 5 });
    const onConnected = vi.fn();
    const { rerender } = await open({ onConnected });

    await advance(5_000);
    expect(pollDeviceAuth).toHaveBeenCalledTimes(1);

    // They close it, then think better of it and open it again -- which starts
    // a SECOND flow, with its own code and its own handle.
    rerender(<DeviceCodeDialog {...props} open={false} onConnected={onConnected} />);
    await act(async () => {});
    rerender(<DeviceCodeDialog {...props} onConnected={onConnected} />);
    await act(async () => {});
    expect(screen.getByText("ABCD-1234")).toBeTruthy();

    // Only NOW does the abandoned flow's request come back, and it says the
    // account connected. Landing it is what a stale poll does; acting on it
    // would connect an account against a handle nobody is waiting on, close the
    // dialog under the operator's second attempt, and toast about it.
    await act(async () => {
      land({ state: "connected", account: { accountName: "Dallas" } });
    });

    expect(onConnected).not.toHaveBeenCalled();
    expect(toastSuccess).not.toHaveBeenCalled();
    // The second flow is untouched and still waiting for its own code.
    expect(screen.getByText("ABCD-1234")).toBeTruthy();
  });

  it("starts over from the failure, without making the operator reopen it", async () => {
    startDeviceAuth
      .mockRejectedValueOnce(new Error("twitch is down"))
      .mockResolvedValue({ ...AUTH });
    pollDeviceAuth.mockResolvedValue({ state: "pending" });
    await open();

    expect(screen.getByText("twitch is down")).toBeTruthy();
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    });
    expect(startDeviceAuth).toHaveBeenCalledTimes(2);
    expect(screen.getByText("ABCD-1234")).toBeTruthy();
  });

  it("hands the dialog back to the caller when the operator cancels", async () => {
    pollDeviceAuth.mockResolvedValue({ state: "pending" });
    const onOpenChange = vi.fn();
    const onConnected = vi.fn();
    await open({ onOpenChange, onConnected });

    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(onOpenChange).toHaveBeenCalledWith(false);
    // Cancelling connected nothing, so the caller has nothing to reload.
    expect(onConnected).not.toHaveBeenCalled();
  });

  // A rejection that is not an Error still has to become a sentence. An empty
  // warning line is the one outcome that tells the operator nothing at all.
  it("says something even when the failure was not an Error", async () => {
    startDeviceAuth.mockRejectedValue("boom");
    await open();

    expect(
      screen.getByText("Could not start the device authorisation."),
    ).toBeTruthy();
  });
});

describe("UserCode's copy button", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    cleanup();
    vi.useRealTimers();
    vi.clearAllMocks();
  });

  const setClipboard = (value: unknown) =>
    Object.defineProperty(navigator, "clipboard", { value, configurable: true });

  const copyButton = () => screen.getByRole("button", { name: "Copy" });
  /** The icon swap is the entire acknowledgement -- there is no text to read,
   *  by design, because the button sits beside a code that must stay the
   *  biggest thing in the dialog. */
  const icon = () => copyButton().querySelector("svg")?.getAttribute("class") ?? "";

  it("copies the code, then says so, then offers to do it again", async () => {
    const writeText = vi.fn(async () => {});
    setClipboard({ writeText });
    render(<UserCode code="ABCD-1234" />);

    expect(icon()).toContain("lucide-copy");
    await act(async () => {
      fireEvent.click(copyButton());
    });
    expect(writeText).toHaveBeenCalledWith("ABCD-1234");
    expect(icon()).toContain("lucide-check");

    // It goes back. A button stuck on a tick reads as "already done" and gives
    // the operator no answer at all the second time they press it.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_500);
    });
    expect(icon()).toContain("lucide-copy");
  });

  // THE POPULATION THIS WHOLE FEATURE EXISTS FOR. A self-hosted box on plain
  // HTTP is an insecure origin, where the browser does not give the page a
  // clipboard at all -- which is exactly the operator who has no registrable
  // callback either. The code stays selectable and nothing throws at them.
  it("stays usable where the browser gives the page no clipboard", () => {
    setClipboard(undefined);
    render(<UserCode code="ABCD-1234" />);

    expect(() => fireEvent.click(copyButton())).not.toThrow();
    expect(screen.getByText("ABCD-1234")).toBeTruthy();
    expect(icon()).toContain("lucide-copy");
  });

  it("says nothing rather than claiming a copy the browser refused", async () => {
    setClipboard({ writeText: vi.fn(async () => Promise.reject(new Error("denied"))) });
    render(<UserCode code="ABCD-1234" />);

    await act(async () => {
      fireEvent.click(copyButton());
    });
    // No tick: the operator has not got the code, and a confirmation here would
    // send them to paste an empty clipboard.
    expect(icon()).toContain("lucide-copy");
    expect(screen.getByText("ABCD-1234")).toBeTruthy();
  });
});
