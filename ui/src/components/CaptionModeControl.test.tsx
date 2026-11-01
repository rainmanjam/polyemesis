// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

import { CaptionModeControl } from "./CaptionModeControl";
import en from "@/lib/i18n/en.json";

// The #8 caption picker in the destination dialog. Three promises the operator
// relies on: Kick is told in words rather than offered a route that does not
// exist, a platform shows only the routes the server accepts, and the YouTube
// POST route says whether its caption ingestion URL is stored and what fetching
// one costs (the stream key changes once).
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

  it("draws the picker for Twitch and says captions go out with the stream", () => {
    render(<CaptionModeControl platform="twitch" value="sei" onChange={() => {}} delaySeconds={0} />);
    expect(document.getElementById("dest-caption-mode")).not.toBeNull();
    expect(screen.getByText(en["wave3.captions.onAir"])).toBeTruthy();
    expect(screen.queryByTestId("caption-post-missing")).toBeNull();
    expect(screen.queryByTestId("caption-kick-none")).toBeNull();
  });

  // MUTATION: the onAir line drawn whatever the route. Observed: FAIL, a
  // destination with captions off was told they go out with its stream.
  it("does not say captions go out with the stream when the route is off", () => {
    render(<CaptionModeControl platform="twitch" value="off" onChange={() => {}} delaySeconds={0} />);
    expect(screen.queryByText(en["wave3.captions.onAir"])).toBeNull();
  });

  // The server refuses a caption-route change on a live destination (409) and
  // restarts an enabled one that is not yet delivering, so the picker says both
  // before the operator presses Save.
  it("says a live destination must be stopped to change its route", () => {
    render(<CaptionModeControl platform="twitch" value="sei" onChange={() => {}} delaySeconds={0} />);
    expect(screen.getByTestId("caption-change-needs-stop").textContent).toBe(en["wave3.captions.changeNeedsStop"]);
  });

  // MUTATION: the restart line shown for any changed route. Observed: FAIL, a
  // change from off to YouTube POST, which touches no video, said it restarts.
  it("says a save restarts the destination only when the change adds or removes the in-stream stage", () => {
    const { rerender } = render(
      <CaptionModeControl platform="twitch" value="sei" storedMode="off" onChange={() => {}} delaySeconds={0} />,
    );
    expect(screen.getByTestId("caption-change-restarts").textContent).toBe(en["wave3.captions.changeRestarts"]);
    rerender(
      <CaptionModeControl platform="youtube" value="youtube_post" storedMode="off" onChange={() => {}} delaySeconds={30} />,
    );
    expect(screen.queryByTestId("caption-change-restarts")).toBeNull();
    rerender(
      <CaptionModeControl platform="youtube" value="youtube_post" storedMode="sei" onChange={() => {}} delaySeconds={30} />,
    );
    expect(screen.getByTestId("caption-change-restarts")).toBeTruthy();
    rerender(<CaptionModeControl platform="twitch" value="sei" storedMode="sei" onChange={() => {}} delaySeconds={0} />);
    expect(screen.queryByTestId("caption-change-restarts")).toBeNull();
  });

  it("asks for the 30 s or 60 s delay when YouTube's caption POST is chosen at another delay", () => {
    render(<CaptionModeControl platform="youtube" value="youtube_post" onChange={() => {}} delaySeconds={0} />);
    expect(screen.getByText(en["wave3.captions.delayNote"])).toBeTruthy();
  });

  // MUTATION: the postDelayYouTube note dropped. Observed: FAIL, nothing said
  // who applies the delay.
  it("says YouTube applies a met 30 s or 60 s delay, so viewers are not twice it behind", () => {
    render(<CaptionModeControl platform="youtube" value="youtube_post" onChange={() => {}} delaySeconds={60} />);
    expect(screen.getByTestId("caption-post-delay").textContent).toBe(
      en["wave3.captions.postDelayYouTube"].replaceAll("{seconds}", "60"),
    );
  });

  // MUTATION: the output-delay comparison dropped from the restart note.
  // Observed: FAIL, no restart was announced when the delay moved.
  it("warns that switching a delayed YouTube destination to or from caption POST restarts it", () => {
    const { rerender } = render(
      <CaptionModeControl platform="youtube" value="youtube_post" storedMode="off" onChange={() => {}} delaySeconds={30} />,
    );
    expect(screen.getByTestId("caption-change-moves-delay")).toBeTruthy();
    rerender(
      <CaptionModeControl platform="youtube" value="off" storedMode="youtube_post" onChange={() => {}} delaySeconds={30} />,
    );
    expect(screen.getByTestId("caption-change-moves-delay")).toBeTruthy();
    // No delay to move: nothing restarts.
    rerender(
      <CaptionModeControl platform="youtube" value="off" storedMode="youtube_post" onChange={() => {}} delaySeconds={0} />,
    );
    expect(screen.queryByTestId("caption-change-moves-delay")).toBeNull();
  });

  it("does not ask for a delay once the delay is 30 s or 60 s", () => {
    render(<CaptionModeControl platform="youtube" value="youtube_post" onChange={() => {}} delaySeconds={60} />);
    expect(screen.queryByText(en["wave3.captions.delayNote"])).toBeNull();
  });

  // MUTATION: the ingestReady branch inverted. Observed: FAIL, the missing-URL
  // warning showed with a URL stored.
  it("warns that a YouTube POST destination with no caption URL needs Refresh stream key, which moves the key once", () => {
    render(<CaptionModeControl platform="youtube" value="youtube_post" onChange={() => {}} delaySeconds={30} />);
    expect(screen.getByTestId("caption-post-missing").textContent).toBe(en["wave3.captions.postMissing"]);
    expect(screen.queryByTestId("caption-post-ready")).toBeNull();
    expect(screen.getByText(en["wave3.captions.postBroadcast"])).toBeTruthy();
  });

  it("says the caption URL is stored when the saved destination holds one", () => {
    render(
      <CaptionModeControl platform="youtube" value="youtube_post" onChange={() => {}} delaySeconds={30} ingestReady />,
    );
    expect(screen.getByTestId("caption-post-ready").textContent).toBe(en["wave3.captions.postReady"]);
    expect(screen.queryByTestId("caption-post-missing")).toBeNull();
  });

  it("says nothing about a caption URL for the embedded route", () => {
    render(<CaptionModeControl platform="youtube" value="sei" onChange={() => {}} delaySeconds={0} />);
    expect(screen.queryByTestId("caption-post-missing")).toBeNull();
    expect(screen.queryByTestId("caption-post-ready")).toBeNull();
  });
});
