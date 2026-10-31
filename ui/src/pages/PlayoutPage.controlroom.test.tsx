// @vitest-environment jsdom

import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import {
  AdvancedSection,
  ExposureBanner,
  PackagingCard,
  PlayerPageCard,
  ShareCard,
  VariantsCard,
} from "./PlayoutPage";
import type { PlayoutAdminView, PlayoutSettings } from "@/lib/types";

/* PLAYOUT READS AS A CONTROL ROOM (app-council A16-A20).
 *
 * A16: each of eight packaging fields committed its own PUT on blur, and each
 *      PUT restarted the muxers, so tabbing through two fields on air was two
 *      interruptions. The fields are one draft now, applied by one button that
 *      appears only while something changed, and that asks first when anyone
 *      is watching.
 * A17: a status line under the title says on/off, who can watch and how many
 *      are watching; Ladder and Packaging sit in a closed Advanced section.
 * A18: the off state has a primary "Turn on playout" action, and the share
 *      links are greyed with a visible reason while they serve nothing.
 * A20: title and description saved on blur with no feedback. They are a draft
 *      with Save and a save status now. */

const play = (over: Partial<PlayoutSettings> = {}): PlayoutSettings =>
  ({
    enabled: true,
    public: false,
    allowCrossOrigin: false,
    format: "hls",
    segmentSeconds: 4,
    playlistSegments: 6,
    dvrWindowSeconds: 0,
    maxDiskMb: 2048,
    audioKbps: 128,
    sessionIdleSeconds: 30,
    maxSessions: 100,
    variants: [{ name: "main", enabled: true, renditionId: null, audioTrack: 0 }],
    ...over,
  }) as PlayoutSettings;

const view = (over: { enabled?: boolean; public?: boolean; viewers?: number } = {}) =>
  ({
    running: true,
    exposed: false,
    protection: "token",
    title: "",
    description: "",
    urls: { watch: "/w", master: "/m.m3u8", embed: "<iframe>" },
    status: {
      enabled: over.enabled ?? true,
      public: over.public ?? false,
      analytics: { viewers: over.viewers ?? 0, peak: 0, sessions: 3, uncounted: 0 },
    },
  }) as unknown as PlayoutAdminView;

afterEach(cleanup);

// jsdom has no scrollIntoView, which the Radix select calls when it opens.
beforeAll(() => {
  Element.prototype.scrollIntoView = () => {};
});

describe("PackagingCard (app-council A16)", () => {
  it("does not write anything when a field changes or loses focus", () => {
    const onApply = vi.fn((_next: PlayoutSettings) => Promise.resolve());
    render(<PackagingCard play={play()} busy={false} viewers={0} onApply={onApply} />);

    const seg = screen.getByLabelText("Segment length (s)");
    fireEvent.change(seg, { target: { value: "2" } });
    fireEvent.blur(seg);
    const disk = screen.getByLabelText("Disk cap (MB)");
    fireEvent.change(disk, { target: { value: "4096" } });
    fireEvent.blur(disk);

    expect(onApply).not.toHaveBeenCalled();
  });

  it("shows one apply bar only while dirty, counting the changes and what each field was", () => {
    render(<PackagingCard play={play()} busy={false} viewers={0} onApply={vi.fn()} />);
    expect(screen.queryByTestId("packaging-apply-bar")).toBeNull();

    fireEvent.change(screen.getByLabelText("Segment length (s)"), { target: { value: "2" } });
    fireEvent.change(screen.getByLabelText("Disk cap (MB)"), { target: { value: "4096" } });

    expect(screen.getByTestId("packaging-apply-bar")).toBeTruthy();
    expect(screen.getByText("Changes not applied: 2")).toBeTruthy();
    expect(screen.getByText("was 4")).toBeTruthy();
    expect(screen.getByText("was 2048")).toBeTruthy();
  });

  it("applies every change in one write when nobody is watching, and says Saved", async () => {
    const onApply = vi.fn((_next: PlayoutSettings) => Promise.resolve());
    render(<PackagingCard play={play()} busy={false} viewers={0} onApply={onApply} />);
    fireEvent.change(screen.getByLabelText("Segment length (s)"), { target: { value: "2" } });
    fireEvent.change(screen.getByLabelText("Disk cap (MB)"), { target: { value: "4096" } });

    fireEvent.click(screen.getByRole("button", { name: "Apply and restart stream" }));

    await waitFor(() => expect(onApply).toHaveBeenCalledTimes(1));
    expect(onApply.mock.calls[0][0]).toMatchObject({ segmentSeconds: 2, maxDiskMb: 4096, playlistSegments: 6 });
    await waitFor(() => expect(screen.getByText("Saved")).toBeTruthy());
  });

  it("asks first, naming the viewers, when anyone is watching", async () => {
    const onApply = vi.fn((_next: PlayoutSettings) => Promise.resolve());
    render(<PackagingCard play={play()} busy={false} viewers={12} onApply={onApply} />);
    fireEvent.change(screen.getByLabelText("Segment length (s)"), { target: { value: "2" } });

    fireEvent.click(screen.getByRole("button", { name: "Apply and restart stream" }));

    const dialog = await screen.findByRole("dialog");
    expect(dialog.textContent).toContain("12");
    expect(onApply).not.toHaveBeenCalled();
  });

  it("refuses to apply a value outside its range, with the reason in words", () => {
    const onApply = vi.fn((_next: PlayoutSettings) => Promise.resolve());
    render(<PackagingCard play={play()} busy={false} viewers={0} onApply={onApply} />);
    fireEvent.change(screen.getByLabelText("Segment length (s)"), { target: { value: "99" } });

    const apply = screen.getByRole("button", { name: "Apply and restart stream" }) as HTMLButtonElement;
    expect(apply.disabled).toBe(true);
    expect(screen.getByText("Enter a number from 1 to 30.")).toBeTruthy();
    expect(screen.getByText("Fix the values outside their range first.")).toBeTruthy();
  });

  it("keeps the draft and says Not saved when the write fails", async () => {
    const onApply = vi.fn((_next: PlayoutSettings) => Promise.reject(new Error("no")));
    render(<PackagingCard play={play()} busy={false} viewers={0} onApply={onApply} />);
    const seg = screen.getByLabelText("Segment length (s)") as HTMLInputElement;
    fireEvent.change(seg, { target: { value: "2" } });

    fireEvent.click(screen.getByRole("button", { name: "Apply and restart stream" }));

    await waitFor(() => expect(screen.getByText("Not saved. Try again.")).toBeTruthy());
    expect(seg.value).toBe("2");
  });
});

describe("Quality level rows (app-council A16, A19)", () => {
  it("renames only on Apply, never on blur", async () => {
    const onSave = vi.fn((_next: PlayoutSettings) => Promise.resolve());
    const onApply = vi.fn((_next: PlayoutSettings) => Promise.resolve());
    render(
      <VariantsCard play={play()} variants={[]} renditions={[]} busy={false} onSave={onSave} onApply={onApply} />,
    );
    const name = screen.getByLabelText("Name");
    fireEvent.change(name, { target: { value: "hd" } });
    fireEvent.blur(name);
    expect(onSave).not.toHaveBeenCalled();
    expect(onApply).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Apply" }));
    await waitFor(() => expect(onApply).toHaveBeenCalledTimes(1));
    expect(onApply.mock.calls[0][0].variants[0].name).toBe("hd");
  });

  it("uses plain labels for the ladder and the source choice", () => {
    render(<VariantsCard play={play()} variants={[]} renditions={[]} busy={false} onSave={vi.fn()} />);
    expect(screen.getByText("Quality levels")).toBeTruthy();
    expect(screen.getByText("Audio track (number, from 0)")).toBeTruthy();
    expect(screen.queryByText("Ladder")).toBeNull();
    expect(screen.queryByText(/bit-for-bit/)).toBeNull();
  });
});

describe("AdvancedSection (app-council A17)", () => {
  it("starts closed, and stays open while packaging holds unapplied changes", () => {
    render(
      <AdvancedSection
        play={play()}
        variants={[]}
        renditions={[]}
        busy={false}
        viewers={0}
        onSave={vi.fn()}
        onApply={vi.fn()}
      />,
    );
    const toggle = screen.getByRole("button", { name: /Advanced/ });
    expect(toggle.getAttribute("aria-expanded")).toBe("false");

    fireEvent.click(toggle);
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    fireEvent.change(screen.getByLabelText("Rewind window (s)"), { target: { value: "60" } });

    fireEvent.click(toggle);
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(screen.getByText("Apply or discard the changes below before closing this section.")).toBeTruthy();
  });
});

describe("AdvancedSection opens itself when the answer is inside", () => {
  const section = (p: PlayoutSettings, variants: PlayoutAdminView["status"]["variants"] = []) =>
    render(
      <AdvancedSection play={p} variants={variants} renditions={[]} busy={false} viewers={0} onSave={vi.fn()} onApply={vi.fn()} />,
    );

  it("starts open with no quality levels, since nothing is served without one", () => {
    section(play({ variants: [] }));
    expect(screen.getByRole("button", { name: /Advanced/ }).getAttribute("aria-expanded")).toBe("true");
    expect(screen.getByRole("button", { name: "Add a quality level" })).toBeTruthy();
  });

  it("opens and stays open while a level reports an error", () => {
    section(play(), [
      { name: "main", audioTrack: 0, running: false, bandwidth: 0, playlist: "", viewers: 0, error: "muxer exited" },
    ] as never);
    const toggle = screen.getByRole("button", { name: /Advanced/ });
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    fireEvent.click(toggle);
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(screen.getByText("muxer exited")).toBeTruthy();
  });
});

describe("Status line and the off state (app-council A17, A18)", () => {
  it("says playout is on, who can watch and how many are watching", () => {
    render(<ExposureBanner view={view({ viewers: 7 })} busy={false} onTurnOn={vi.fn()} />);
    expect(screen.getByText("Playout is on")).toBeTruthy();
    expect(screen.getByText("Private.")).toBeTruthy();
    expect(screen.getByText("7")).toBeTruthy();
  });

  it("offers Turn on playout when it is off", () => {
    const onTurnOn = vi.fn();
    render(<ExposureBanner view={view({ enabled: false })} busy={false} onTurnOn={onTurnOn} />);
    expect(screen.getByText("Playout is off")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Turn on playout" }));
    expect(onTurnOn).toHaveBeenCalledTimes(1);
  });

  it("greys the share links while playout is off, with the reason in words", () => {
    render(<ShareCard view={view()} play={play({ enabled: false })} busy={false} onRotate={vi.fn()} />);
    const reason = screen.getByText("Playout is off, so these links serve nothing yet. Turn it on first.");
    expect(reason).toBeTruthy();
    const copy = screen.getByRole("button", { name: /Copy Player page/ }) as HTMLButtonElement;
    expect(copy.disabled).toBe(true);
  });
});

describe("PlayerPageCard (app-council A20)", () => {
  it("does not save on blur; Save writes the draft and says Saved", async () => {
    const onSave = vi.fn((_b: { title: string; description: string }) => Promise.resolve());
    render(<PlayerPageCard title="" description="" onSave={onSave} />);
    const title = screen.getByLabelText("Title");
    fireEvent.change(title, { target: { value: "Friday show" } });
    fireEvent.blur(title);
    expect(onSave).not.toHaveBeenCalled();
    expect(screen.getByText("Unsaved changes")).toBeTruthy();

    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Save" }));
    });
    expect(onSave).toHaveBeenCalledWith({ title: "Friday show", description: "" });
    await waitFor(() => expect(screen.getByText("Saved")).toBeTruthy());
  });
});
