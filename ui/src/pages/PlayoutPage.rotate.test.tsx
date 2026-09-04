// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { ShareCard } from "./PlayoutPage";
import { translate } from "@/lib/i18n";
import type { PlayoutAdminView, PlayoutSettings } from "@/lib/types";

/* ONE UNCONFIRMED CLICK REVOKED EVERY SHARED LINK.
 *
 * "Generate a new link" was a ghost button wired straight to
 * api.rotatePlayoutToken(). The moment it landed, the player page, the HLS
 * playlist and the embed code all stopped working, and everyone pulling
 * segments was dropped mid-stream. The old token cannot be brought back, so
 * there is nothing to undo to.
 *
 * On the same page, removing a ladder rung -- three fields to rebuild -- asks
 * first and names the viewers it drops. That inconsistency is itself the
 * defect: an operator who learns "the destructive things ask" and then meets
 * one that does not has had their caution trained out of them exactly where it
 * mattered. */

const play = () =>
  ({ enabled: true, public: true, allowCrossOrigin: true }) as PlayoutSettings;

const view = (viewers: number) =>
  ({
    running: true,
    protection: "token",
    urls: { watch: "/w", master: "/m.m3u8", embed: "<iframe>" },
    status: { analytics: { viewers, peak: viewers, sessions: 1, uncounted: 0 } },
  }) as unknown as PlayoutAdminView;

afterEach(cleanup);

const NEW_LINK = translate("en", "play.rotate");

describe("ShareCard: generating a new playback link", () => {
  it("does not rotate the token on the click itself", () => {
    const onRotate = vi.fn().mockResolvedValue(undefined);
    render(<ShareCard view={view(41)} play={play()} busy={false} onRotate={onRotate} />);

    fireEvent.click(screen.getByRole("button", { name: NEW_LINK }));

    expect(
      onRotate,
      "the rotation went out on the first click, dropping every live viewer",
    ).not.toHaveBeenCalled();
  });

  it("asks first, naming the viewers it is about to drop", async () => {
    const onRotate = vi.fn().mockResolvedValue(undefined);
    render(<ShareCard view={view(41)} play={play()} busy={false} onRotate={onRotate} />);

    fireEvent.click(screen.getByRole("button", { name: NEW_LINK }));

    const dialog = await screen.findByRole("dialog");
    // The count that makes this a decision rather than a click.
    expect(dialog.textContent).toContain("41");
    expect(dialog.textContent).toContain(translate("en", "play.rotateDrops"));

    fireEvent.click(
      screen.getAllByRole("button", { name: NEW_LINK }).at(-1) as HTMLElement,
    );
    await waitFor(() => expect(onRotate).toHaveBeenCalledTimes(1));
  });

  it("leaves the token alone when the dialog is cancelled", async () => {
    const onRotate = vi.fn().mockResolvedValue(undefined);
    render(<ShareCard view={view(41)} play={play()} busy={false} onRotate={onRotate} />);

    fireEvent.click(screen.getByRole("button", { name: NEW_LINK }));
    await screen.findByRole("dialog");
    fireEvent.click(
      screen.getByRole("button", { name: translate("en", "common.cancel") }),
    );

    expect(onRotate).not.toHaveBeenCalled();
  });

  /* A row reading "Watching now 0" says "this does nothing", and that is
   * false: every link already shared dies whether or not a player happens to
   * be pulling segments at that second. */
  it("draws no viewer row when nobody is watching", async () => {
    const onRotate = vi.fn().mockResolvedValue(undefined);
    render(<ShareCard view={view(0)} play={play()} busy={false} onRotate={onRotate} />);

    fireEvent.click(screen.getByRole("button", { name: NEW_LINK }));
    const dialog = await screen.findByRole("dialog");
    expect(dialog.textContent).not.toContain(translate("en", "play.rotateDrops"));
    // The sentence that says what leaves is still there.
    expect(dialog.textContent).toContain(translate("en", "play.rotateDescription"));
  });
});
