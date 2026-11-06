// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ThemeProvider } from "./ThemeProvider";
import { ThemeToggle } from "./ThemeToggle";
import { THEME_STORAGE_KEY } from "@/lib/theme";
import en from "@/lib/i18n/en.json";

/* A three-way segmented control: Light / Dark / System. A radiogroup, because
 * exactly one is chosen and the arrow keys move between them. */

const mount = () =>
  render(
    <ThemeProvider>
      <ThemeToggle />
    </ThemeProvider>,
  );
const radios = () => screen.getAllByRole("radio");

beforeEach(() => {
  localStorage.clear();
  document.documentElement.removeAttribute("data-theme");
  history.replaceState(null, "", "/");
  vi.stubGlobal("matchMedia", (q: string) => ({
    matches: false,
    media: q,
    addEventListener: () => {},
    removeEventListener: () => {},
  }));
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("ThemeToggle", () => {
  it("is a labelled radiogroup of Light, Dark and System, in that order", () => {
    mount();
    expect(screen.getByRole("radiogroup", { name: en["chrome.theme"] })).toBeTruthy();
    expect(radios().map((r) => r.getAttribute("aria-label"))).toEqual([
      en["chrome.themeLight"],
      en["chrome.themeDark"],
      en["chrome.themeSystem"],
    ]);
  });

  it("starts on System and says so", () => {
    mount();
    expect(radios().map((r) => r.getAttribute("aria-checked"))).toEqual(["false", "false", "true"]);
  });

  it("starts on the remembered choice", () => {
    localStorage.setItem(THEME_STORAGE_KEY, "light");
    mount();
    expect(radios().map((r) => r.getAttribute("aria-checked"))).toEqual(["true", "false", "false"]);
  });

  it("applies and remembers a click, and moves the checked state", () => {
    mount();
    fireEvent.click(radios()[0]);
    expect(document.documentElement.dataset.theme).toBe("light");
    expect(localStorage.getItem(THEME_STORAGE_KEY)).toBe("light");
    expect(radios()[0].getAttribute("aria-checked")).toBe("true");
    expect(radios()[2].getAttribute("aria-checked")).toBe("false");
    fireEvent.click(radios()[1]);
    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(radios()[1].getAttribute("aria-checked")).toBe("true");
  });

  it("has one tab stop, on the checked option", () => {
    mount();
    expect(radios().map((r) => r.getAttribute("tabindex"))).toEqual(["-1", "-1", "0"]);
    fireEvent.click(radios()[0]);
    expect(radios().map((r) => r.getAttribute("tabindex"))).toEqual(["0", "-1", "-1"]);
  });

  it("moves with the arrow keys, wrapping, and picks as it goes", () => {
    mount();
    // From System (last), ArrowRight wraps to Light.
    act(() => radios()[2].focus());
    fireEvent.keyDown(radios()[2], { key: "ArrowRight" });
    expect(radios()[0].getAttribute("aria-checked")).toBe("true");
    expect(document.activeElement).toBe(radios()[0]);
    fireEvent.keyDown(radios()[0], { key: "ArrowLeft" });
    expect(radios()[2].getAttribute("aria-checked")).toBe("true");
    fireEvent.keyDown(radios()[2], { key: "ArrowUp" });
    expect(radios()[1].getAttribute("aria-checked")).toBe("true");
    fireEvent.keyDown(radios()[1], { key: "ArrowDown" });
    expect(radios()[2].getAttribute("aria-checked")).toBe("true");
  });

  it("jumps to the ends with Home and End", () => {
    mount();
    fireEvent.keyDown(radios()[2], { key: "Home" });
    expect(radios()[0].getAttribute("aria-checked")).toBe("true");
    fireEvent.keyDown(radios()[0], { key: "End" });
    expect(radios()[2].getAttribute("aria-checked")).toBe("true");
  });

  it("leaves other keys alone", () => {
    mount();
    fireEvent.keyDown(radios()[2], { key: "a" });
    expect(radios()[2].getAttribute("aria-checked")).toBe("true");
  });

  it("names every option, so the icon is never the only label", () => {
    mount();
    for (const r of radios()) {
      expect(r.getAttribute("aria-label")).toBeTruthy();
      expect(r.getAttribute("title")).toBe(r.getAttribute("aria-label"));
    }
  });
});
