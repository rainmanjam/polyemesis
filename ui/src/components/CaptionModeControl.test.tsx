// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

import { CaptionModeControl } from "./CaptionModeControl";
import en from "@/lib/i18n/en.json";

// The #8 caption picker in the destination dialog. Three promises the operator
// relies on: Kick is told in words rather than offered a route that does not
// exist, a platform shows only the routes the server accepts, and the picker
// says plainly that the choice is saved but not yet sent.
//
// MUTATION: CaptionModeControl renders the Select for kick too (drop the
// captionsOffered guard). Observed: FAIL, the Kick sentence is absent and a
// caption-mode select is present.

afterEach(() => cleanup());

describe("CaptionModeControl", () => {
  it("tells a Kick operator in words that no caption route exists, and draws no picker", () => {
    render(<CaptionModeControl platform="kick" value="off" onChange={() => {}} delaySeconds={0} />);
    expect(screen.getByTestId("caption-kick-none").textContent).toBe(en["wave3.captions.kickNone"]);
    expect(document.getElementById("dest-caption-mode")).toBeNull();
  });

  it("draws the picker for Twitch and names the saved-but-not-sent limit", () => {
    render(<CaptionModeControl platform="twitch" value="sei" onChange={() => {}} delaySeconds={0} />);
    expect(document.getElementById("dest-caption-mode")).not.toBeNull();
    expect(screen.getByText(en["wave3.captions.storedOnly"])).toBeTruthy();
    expect(screen.queryByTestId("caption-kick-none")).toBeNull();
  });

  it("asks for the 30 s or 60 s delay when YouTube's caption POST is chosen at another delay", () => {
    render(<CaptionModeControl platform="youtube" value="youtube_post" onChange={() => {}} delaySeconds={0} />);
    expect(screen.getByText(en["wave3.captions.delayNote"])).toBeTruthy();
  });

  it("does not ask for a delay once the delay is 30 s or 60 s", () => {
    render(<CaptionModeControl platform="youtube" value="youtube_post" onChange={() => {}} delaySeconds={60} />);
    expect(screen.queryByText(en["wave3.captions.delayNote"])).toBeNull();
  });
});
