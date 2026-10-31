// @vitest-environment jsdom

import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { ProtectionCard } from "./PlayoutPage";
import type { PlayoutSettings } from "@/lib/types";

/* MAKING THE STREAM OPEN IS A DECISION ABOUT WHO CAN WATCH (A15).
 *
 * The Public switch and the Protection select both saved the moment they were
 * touched, so one click exposed the stream to people who are not signed in.
 * Removing a rung already asked first; turning exposure ON now asks too, and
 * says who can watch. Turning exposure off does not ask. */

const play = (over: Partial<PlayoutSettings> = {}): PlayoutSettings =>
  ({
    enabled: true,
    public: false,
    allowCrossOrigin: false,
    format: "hls",
    segmentSeconds: 4,
    playlistSegments: 6,
    dvrWindowSeconds: 0,
    maxDiskMb: 1024,
    audioKbps: 128,
    sessionIdleSeconds: 30,
    maxSessions: 100,
    variants: [],
    ...over,
  }) as PlayoutSettings;

const view = (protection: "token" | "open") =>
  ({ protection, status: { enabled: true, public: false } }) as never;

afterEach(cleanup);

// jsdom has no scrollIntoView, which the Radix select calls when it opens.
beforeAll(() => {
  Element.prototype.scrollIntoView = () => {};
});

describe("ProtectionCard exposure changes (A15)", () => {
  it("asks before Public goes on, and does not save until confirmed", async () => {
    const onSaveSettings = vi.fn((_next: PlayoutSettings) => Promise.resolve());
    const onSave = vi.fn((_body: { protection?: "token" | "open" }) => Promise.resolve());
    render(
      <ProtectionCard view={view("token")} play={play()} busy={false} onSave={onSave} onSaveSettings={onSaveSettings} />,
    );

    fireEvent.click(screen.getByRole("switch", { name: "Serve viewers without a session" }));

    await waitFor(() =>
      expect(screen.getByText("Serve this stream to people who are not signed in?")).toBeTruthy(),
    );
    expect(onSaveSettings).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Turn on public viewing" }));
    await waitFor(() => expect(onSaveSettings).toHaveBeenCalledTimes(1));
    expect(onSaveSettings.mock.calls[0][0]).toMatchObject({ public: true });
  });

  it("asks before Protection goes open, and does not save until confirmed", async () => {
    const onSave = vi.fn((_body: { protection?: "token" | "open" }) => Promise.resolve());
    render(
      <ProtectionCard view={view("token")} play={play()} busy={false} onSave={onSave} onSaveSettings={vi.fn((_next: PlayoutSettings) => Promise.resolve())} />,
    );

    fireEvent.click(screen.getByRole("combobox", { name: "Protection" }));
    fireEvent.click(await screen.findByRole("option", { name: /Open/ }));

    await waitFor(() =>
      expect(screen.getByText("Let anyone who can reach this server watch?")).toBeTruthy(),
    );
    expect(onSave).not.toHaveBeenCalled();
  });
});
