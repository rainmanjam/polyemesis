// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

import { PublishClipDialog } from "./PublishClipDialog";
import type { Publication, PublishPlatform, PublishPrepared, PublishTargets } from "@/lib/publish";

const api = vi.hoisted(() => ({
  publishTargets: vi.fn(),
  preparePublish: vi.fn(),
  publishClip: vi.fn(),
  publication: vi.fn(),
}));

vi.mock("@/lib/api", () => ({
  api,
  ApiError: class extends Error {
    status: number;
    code: string;
    constructor(status: number, message: string, code = "") {
      super(message);
      this.status = status;
      this.code = code;
    }
  },
}));

const youtube: PublishPlatform = {
  id: "youtube", enabled: true, configured: true, quotaUnits: 1600,
  privacy: { default: "private", options: ["private", "unlisted", "public"], forced: false },
  accounts: [{ id: 7, name: "My Channel" }],
};
const tiktok: PublishPlatform = {
  id: "tiktok", enabled: true, configured: true,
  privacy: { default: "SELF_ONLY", options: ["SELF_ONLY"], forced: true },
  accounts: [{ id: 8, name: "creator" }],
};
const xplat: PublishPlatform = {
  id: "x", enabled: false, gatedReason: "off", configured: false,
  privacy: { default: "public", options: ["public"], forced: true }, accounts: [],
};

function targets(over: Partial<PublishTargets> = {}): PublishTargets {
  return {
    clipJobId: 3, platforms: [youtube, tiktok, xplat], seconds: 20, vertical: true, publications: [],
    prefill: { title: "Big moment", description: "words", tags: ["Shorts"], privacy: "" }, ...over,
  };
}

function prepared(over: Partial<PublishPrepared> = {}): PublishPrepared {
  return {
    confirmToken: "tok-1", expiresAt: new Date(Date.now() + 600_000).toISOString(), platform: "youtube",
    accountId: 7, accountName: "My Channel", meta: { title: "Big moment", description: "words", tags: ["Shorts"], privacy: "private" },
    privacyForced: false, ...over,
  };
}

function pub(over: Partial<Publication> = {}): Publication {
  return {
    id: 11, idempotencyKey: "pub_x", clipJobId: 3, platform: "youtube", accountId: 7, accountName: "My Channel",
    title: "Big moment", description: "words", tags: ["Shorts"], privacy: "private", state: "confirmed", attempts: 0,
    createdAt: "", updatedAt: "", ...over,
  };
}

function open() {
  return render(
    <MemoryRouter>
      <PublishClipDialog clipJobId={3} open onOpenChange={() => undefined} />
    </MemoryRouter>,
  );
}

beforeEach(() => {
  api.publishTargets.mockResolvedValue(targets());
  api.preparePublish.mockImplementation(async (f) => prepared({ meta: { ...f, privacy: f.privacy } }));
});
afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("the publish dialog", () => {
  it("prefills the form from the clip and never posts by itself", async () => {
    open();
    const title = (await screen.findByLabelText("Title")) as HTMLInputElement;
    expect(title.value).toBe("Big moment");
    await waitFor(() => expect(api.preparePublish).toHaveBeenCalled());
    // The form has been checked and the button is live -- and still nothing is posted.
    await waitFor(() => expect((screen.getByRole("button", { name: "Publish" }) as HTMLButtonElement).disabled).toBe(false));
    await new Promise((r) => setTimeout(r, 600));
    expect(api.publishClip).not.toHaveBeenCalled();
  });

  it("posts only on the click, with the confirmation minted for exactly that form", async () => {
    api.publishClip.mockResolvedValue(pub({ state: "posted", postUrl: "https://youtube.example/shorts/abc", actualPrivacy: "private" }));
    open();
    const btn = await screen.findByRole("button", { name: "Publish" });
    await waitFor(() => expect((btn as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(btn);
    await waitFor(() => expect(api.publishClip).toHaveBeenCalledTimes(1));
    const [form, token] = api.publishClip.mock.calls[0];
    expect(token).toBe("tok-1");
    expect(form).toMatchObject({ clipJobId: 3, platform: "youtube", accountId: 7, title: "Big moment", privacy: "private" });
    const result = await screen.findByTestId("publish-result");
    expect(result.textContent).toContain("Posted");
    expect(result.querySelector("a")?.getAttribute("href")).toBe("https://youtube.example/shorts/abc");
  });

  it("drops the confirmation the moment the form is edited", async () => {
    open();
    const btn = (await screen.findByRole("button", { name: "Publish" })) as HTMLButtonElement;
    await waitFor(() => expect(btn.disabled).toBe(false));
    fireEvent.change(screen.getByLabelText("Title"), { target: { value: "Changed words" } });
    expect(btn.disabled).toBe(true);
    await waitFor(() => expect(btn.disabled).toBe(false));
    expect(api.preparePublish.mock.calls.at(-1)?.[0].title).toBe("Changed words");
    expect(api.publishClip).not.toHaveBeenCalled();
  });

  it("sets TikTok private, says why, and offers no choice", async () => {
    open();
    fireEvent.click(await screen.findByRole("radio", { name: "TikTok" }));
    const forced = await screen.findByTestId("publish-privacy-forced");
    expect(forced.textContent).toBe("Only you");
    expect(screen.getByText(/has not audited private to you/)).toBeTruthy();
    expect(screen.queryByRole("radio", { name: "Public" })).toBeNull();
    await waitFor(() => expect(api.preparePublish).toHaveBeenCalled());
    expect(api.preparePublish.mock.calls.at(-1)?.[0]).toMatchObject({ platform: "tiktok", privacy: "SELF_ONLY" });
  });

  it("lets YouTube's privacy be chosen and sends the choice", async () => {
    open();
    fireEvent.click(await screen.findByRole("radio", { name: "Unlisted" }));
    await waitFor(() => expect(api.preparePublish.mock.calls.at(-1)?.[0].privacy).toBe("unlisted"));
  });

  it("shows X as switched off and never checks or posts to it", async () => {
    open();
    const x = (await screen.findByRole("radio", { name: "X" })) as HTMLButtonElement;
    expect(x.disabled).toBe(true);
    // A disabled control says why, beside itself -- even though X can never be the selected one.
    expect((await screen.findByTestId("publish-gated-x")).textContent).toContain("X publishing is switched off");
    expect(api.publishClip).not.toHaveBeenCalled();
    expect(api.preparePublish.mock.calls.every(([f]) => f.platform !== "x")).toBe(true);
  });

  it("says what is missing instead of leaving Publish disabled in silence", async () => {
    open();
    fireEvent.change(await screen.findByLabelText("Title"), { target: { value: "  " } });
    expect(screen.getByText("Add a title to publish.")).toBeTruthy();
    expect((screen.getByRole("button", { name: "Publish" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("asks to reconnect instead of letting a connection that cannot upload publish", async () => {
    api.publishTargets.mockResolvedValue(
      targets({
        platforms: [
          { ...youtube, accounts: [{ id: 7, name: "My Channel", needsReconnect: true, connectPath: "/api/v1/oauth/youtube/start" }] },
          tiktok, xplat,
        ],
      }),
    );
    open();
    const link = await screen.findByRole("link", { name: "Reconnect to allow uploads" });
    expect(link.getAttribute("href")).toBe("/api/v1/oauth/youtube/start");
    await new Promise((r) => setTimeout(r, 500));
    expect(api.preparePublish).not.toHaveBeenCalled();
    expect((screen.getByRole("button", { name: "Publish" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("points at Settings when no account is connected", async () => {
    api.publishTargets.mockResolvedValue(targets({ platforms: [{ ...youtube, accounts: [] }, tiktok, xplat] }));
    open();
    fireEvent.click(await screen.findByRole("radio", { name: "YouTube Shorts" }));
    const link = await screen.findByRole("link", { name: "Connect one in Settings" });
    expect(link.getAttribute("href")).toBe("/settings?tab=platforms");
  });

  it("follows an upload to its end, showing why it is waiting", async () => {
    api.publishClip.mockResolvedValue(pub({ state: "uploading", job: { id: 5, state: "queued", progress: 0.4, attempts: 1, blocked: true, reason: "an ingest is live" } }));
    api.publication.mockResolvedValue(pub({ state: "posted", platformPostId: "abc", postUrl: "https://youtube.example/abc" }));
    open();
    const btn = await screen.findByRole("button", { name: "Publish" });
    await waitFor(() => expect((btn as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(btn);
    const progress = await screen.findByTestId("publish-progress");
    expect(progress.textContent).toContain("Uploading: 40%");
    expect(progress.textContent).toContain("an ingest is live");
    await waitFor(() => expect(screen.getByTestId("publish-result")).toBeTruthy(), { timeout: 5000 });
    expect(api.publication).toHaveBeenCalledWith(11);
  }, 10000);

  it("explains a failure in words and offers to publish again", async () => {
    api.publishClip.mockResolvedValue(
      pub({ state: "failed", reason: { cause: "quota_exceeded", line: "YouTube's daily allowance is used up" } }),
    );
    open();
    const btn = await screen.findByRole("button", { name: "Publish" });
    await waitFor(() => expect((btn as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(btn);
    const failure = await screen.findByTestId("publish-failure");
    expect(failure.textContent).toContain("daily allowance is used up. Publish again after it resets");
    fireEvent.click(screen.getByRole("button", { name: /Publish again/ }));
    // Back to the form: a new confirmation is needed before anything is sent.
    expect(await screen.findByLabelText("Title")).toBeTruthy();
    expect(api.publishClip).toHaveBeenCalledTimes(1);
  });

  it("reports a refused click from the server without pretending it posted", async () => {
    const { ApiError } = await import("@/lib/api");
    api.publishClip.mockRejectedValue(new (ApiError as unknown as new (s: number, m: string, c: string) => Error)(403, "confirmation not valid", "confirm_required"));
    open();
    const btn = await screen.findByRole("button", { name: "Publish" });
    await waitFor(() => expect((btn as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(btn);
    expect((await screen.findByRole("alert")).textContent).toContain("confirmation not valid");
    expect(screen.queryByTestId("publish-result")).toBeNull();
  });

  it("will not offer a plain retry after an outcome it cannot know, and sends the acknowledgement once ticked", async () => {
    api.publishClip.mockResolvedValueOnce(
      pub({ state: "failed", reason: { cause: "outcome_unknown", line: "check the account" } }),
    );
    open();
    const btn = await screen.findByRole("button", { name: "Publish" });
    await waitFor(() => expect((btn as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(btn);
    await screen.findByTestId("publish-failure");
    const again = screen.getByRole("button", { name: /Publish again/ }) as HTMLButtonElement;
    expect(again.disabled).toBe(true);
    fireEvent.click(screen.getByRole("checkbox"));
    expect(again.disabled).toBe(false);
    fireEvent.click(again);
    await waitFor(() => expect(api.preparePublish.mock.calls.at(-1)?.[0].acknowledgeOutcome).toBe(true));
  });

  it("checks the form again after a refused click instead of leaving Publish stuck", async () => {
    const { ApiError } = await import("@/lib/api");
    api.publishClip.mockRejectedValueOnce(new (ApiError as unknown as new (s: number, m: string, c: string) => Error)(403, "expired", "confirm_required"));
    open();
    const btn = await screen.findByRole("button", { name: "Publish" });
    await waitFor(() => expect((btn as HTMLButtonElement).disabled).toBe(false));
    const before = api.preparePublish.mock.calls.length;
    fireEvent.click(btn);
    await screen.findByRole("alert");
    await waitFor(() => expect(api.preparePublish.mock.calls.length).toBeGreaterThan(before));
    await waitFor(() => expect((screen.getByRole("button", { name: "Publish" }) as HTMLButtonElement).disabled).toBe(false));
  });

  it("survives a server that lists no platforms", async () => {
    api.publishTargets.mockResolvedValue(targets({ platforms: [] }));
    open();
    expect(await screen.findByLabelText("Platform")).toBeTruthy();
  });
});
