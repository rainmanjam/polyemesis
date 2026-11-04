// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, screen } from "@testing-library/react";
import { setLanguage, translate } from "@/lib/i18n";
import type { HeaderLive } from "@/lib/headerLive";

/* The phone on-air chip: which words it prints, and what it keeps for a
 * screen reader. Whether the full words fit is measured by lib/chipFit (its
 * own tests pin the arithmetic; the e2e spec measures the real header); here
 * that answer is set, and a ResizeObserver stub delivers it the way the
 * browser would, on observe. */

let fits = true;
vi.mock("@/lib/chipFit", () => ({ chipFits: () => fits }));

const observers: Array<() => void> = [];
class StubResizeObserver {
  constructor(cb: () => void) {
    observers.push(cb);
  }
  observe() {}
  disconnect() {}
}

const { MobileOnAirChip } = await import("./MobileOnAirChip");

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  observers.length = 0;
  fits = true;
});

const scoped = (count: number): HeaderLive => ({
  known: true,
  count,
  key: "chrome.liveCountProgramme",
  params: { programme: "Main Programme", count },
  offKey: "chrome.offAirProgramme",
  offParams: { programme: "Main Programme" },
});

function show(live: HeaderLive) {
  return render(
    <header>
      <MobileOnAirChip live={live} />
    </header>,
  );
}

const en = (k: Parameters<typeof translate>[1], p?: Parameters<typeof translate>[2]) => translate("en", k, p);

describe("MobileOnAirChip", () => {
  it("prints the full words while they fit", () => {
    vi.stubGlobal("ResizeObserver", StubResizeObserver);
    show(scoped(0));
    act(() => observers.forEach((cb) => cb()));
    const words = screen.getByTestId("mobile-on-air-words");
    expect(words.textContent).toBe(en("onAir.offAir"));
    expect(words.getAttribute("data-fit")).toBe("full");
  });

  it("prints the short form, whole, where the full words do not fit", () => {
    fits = false;
    vi.stubGlobal("ResizeObserver", StubResizeObserver);
    show(scoped(0));
    act(() => observers.forEach((cb) => cb()));
    const words = screen.getByTestId("mobile-on-air-words");
    expect(words.textContent).toBe(en("chrome.offAirShort"));
    expect(words.getAttribute("data-fit")).toBe("short");
    expect(words.textContent).not.toContain("…");
    expect(words.className).not.toContain("truncate");
  });

  it("prints the live count's short form when on air and short of room", () => {
    fits = false;
    vi.stubGlobal("ResizeObserver", StubResizeObserver);
    show(scoped(3));
    act(() => observers.forEach((cb) => cb()));
    expect(screen.getByTestId("mobile-on-air-words").textContent).toBe(en("chrome.liveCountShort", { count: 3 }));
  });

  it("keeps the whole sentence, programme included, for a screen reader and in the title", () => {
    fits = false;
    vi.stubGlobal("ResizeObserver", StubResizeObserver);
    const { container } = show(scoped(3));
    act(() => observers.forEach((cb) => cb()));
    const chip = screen.getByTestId("mobile-on-air");
    const sentence = en("chrome.liveCountProgramme", { programme: "Main Programme", count: 3 });
    expect(container.querySelector(".sr-only")?.textContent).toBe(sentence);
    expect(chip.getAttribute("title")).toBe(sentence);
    expect(screen.getByTestId("mobile-on-air-words").getAttribute("aria-hidden")).toBe("true");
  });

  // Row 10: on air is the tally lamp everywhere, and this dot is the phone's
  // on-air mark. Green (--ok) means healthy / signal present, not on air.
  it("lights the on-air dot in the tally red, not the healthy green", () => {
    for (const live of [scoped(3), { known: true, count: 2, key: "chrome.liveCount", params: { count: 2 }, offKey: "onAir.offAir" } as HeaderLive]) {
      const { unmount } = show(live);
      const dot = screen.getByTestId("mobile-on-air-dot");
      const cls = dot.className.split(/\s+/);
      expect(cls).toContain("bg-tally");
      expect(cls.some((c) => /^(bg|text|border)-ok\b/.test(c))).toBe(false);
      // A filled disc: its shape, not only its hue, tells it from off air.
      expect(cls).toContain("rounded-full");
      expect(dot.getAttribute("data-tone")).toBe("tally");
      unmount();
    }
  });

  it("draws off air as the hollow idle ring, never the tally", () => {
    show(scoped(0));
    const dot = screen.getByTestId("mobile-on-air-dot");
    expect(dot.className).not.toContain("bg-tally");
    expect(dot.getAttribute("data-tone")).toBe("idle");
  });

  it("does not measure, or name the programme twice, on a single-programme install", () => {
    vi.stubGlobal("ResizeObserver", StubResizeObserver);
    show({ known: true, count: 0, key: "chrome.liveCount", params: { count: 0 }, offKey: "onAir.offAir" });
    expect(observers).toHaveLength(0);
    expect(screen.queryByTestId("mobile-on-air-words")).toBeNull();
    expect(screen.getByTestId("mobile-on-air").textContent).toBe(en("onAir.offAir"));
  });
});

describe("MobileOnAirChip: the Polish live count reads right at any count", () => {
  // "{count} transmisji na żywo" was genitive plural: wrong at 1 ("1 transmisji")
  // and at 2 to 4. The siblings' "{count} na żywo" reads right at every count.
  afterEach(() => setLanguage("en"));
  const live = (count: number): HeaderLive =>
    ({ known: true, count, key: "chrome.liveCount", params: { count }, offKey: "onAir.offAir" }) as HeaderLive;

  it("says 1 na żywo", () => {
    setLanguage("pl");
    show(live(1));
    expect(screen.getByTestId("mobile-on-air").textContent).toBe("1 na żywo");
  });

  it("says 2 na żywo", () => {
    setLanguage("pl");
    show(live(2));
    expect(screen.getByTestId("mobile-on-air").textContent).toBe("2 na żywo");
  });
});
