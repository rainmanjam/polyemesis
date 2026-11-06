// @vitest-environment jsdom
import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ThemeProvider, useTheme } from "./ThemeProvider";
import { THEME_STORAGE_KEY } from "@/lib/theme";

/* The provider is the live half of the theme: it applies the resolved theme to
 * <html>, follows the OS while the choice is "system", and remembers what the
 * operator picks. public/theme-init.js did the first paint; this keeps it true. */

type Listener = (e: { matches: boolean }) => void;

/** A controllable prefers-color-scheme: light query. */
function stubSystem(initialLight: boolean) {
  let light = initialLight;
  const listeners = new Set<Listener>();
  vi.stubGlobal("matchMedia", (q: string) => ({
    get matches() {
      return q.includes("light") && light;
    },
    media: q,
    addEventListener: (_: string, l: Listener) => listeners.add(l),
    removeEventListener: (_: string, l: Listener) => listeners.delete(l),
  }));
  return {
    set(next: boolean) {
      light = next;
      act(() => listeners.forEach((l) => l({ matches: next })));
    },
    listeners: () => listeners.size,
  };
}

function Probe() {
  const { choice, theme, setChoice } = useTheme();
  return (
    <div>
      <span data-testid="choice">{choice}</span>
      <span data-testid="theme">{theme}</span>
      <button data-testid="pick-light" onClick={() => setChoice("light")}>pick light</button>
      <button data-testid="pick-dark" onClick={() => setChoice("dark")}>pick dark</button>
      <button data-testid="pick-system" onClick={() => setChoice("system")}>pick system</button>
    </div>
  );
}

const html = () => document.documentElement;
const mount = () =>
  render(
    <ThemeProvider>
      <Probe />
    </ThemeProvider>,
  );

beforeEach(() => {
  localStorage.clear();
  html().removeAttribute("data-theme");
  history.replaceState(null, "", "/");
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("ThemeProvider", () => {
  it("follows the system when nothing is stored: light OS, light console", () => {
    stubSystem(true);
    mount();
    expect(html().dataset.theme).toBe("light");
    expect(screen.getByTestId("choice").textContent).toBe("system");
    expect(screen.getByTestId("theme").textContent).toBe("light");
  });

  it("follows the system when nothing is stored: dark OS, dark console", () => {
    stubSystem(false);
    mount();
    expect(html().dataset.theme).toBe("dark");
  });

  it("lets a stored choice beat the system", () => {
    stubSystem(true);
    localStorage.setItem(THEME_STORAGE_KEY, "dark");
    mount();
    expect(html().dataset.theme).toBe("dark");
    expect(screen.getByTestId("choice").textContent).toBe("dark");
  });

  it("applies a pick at once and remembers it", () => {
    stubSystem(false);
    mount();
    act(() => screen.getByTestId("pick-light").click());
    expect(html().dataset.theme).toBe("light");
    expect(localStorage.getItem(THEME_STORAGE_KEY)).toBe("light");
    act(() => screen.getByTestId("pick-dark").click());
    expect(html().dataset.theme).toBe("dark");
    expect(localStorage.getItem(THEME_STORAGE_KEY)).toBe("dark");
  });

  it("goes back to following the system, and forgets the pick", () => {
    stubSystem(true);
    localStorage.setItem(THEME_STORAGE_KEY, "dark");
    mount();
    act(() => screen.getByTestId("pick-system").click());
    expect(html().dataset.theme).toBe("light");
    expect(localStorage.getItem(THEME_STORAGE_KEY)).toBeNull();
  });

  it("follows the OS live while the choice is system", () => {
    const sys = stubSystem(false);
    mount();
    expect(html().dataset.theme).toBe("dark");
    sys.set(true);
    expect(html().dataset.theme).toBe("light");
    expect(screen.getByTestId("theme").textContent).toBe("light");
    sys.set(false);
    expect(html().dataset.theme).toBe("dark");
  });

  it("ignores the OS once the operator has picked a theme", () => {
    const sys = stubSystem(false);
    mount();
    act(() => screen.getByTestId("pick-dark").click());
    sys.set(true);
    expect(html().dataset.theme).toBe("dark");
    // ...and is not listening at all, rather than listening and discarding.
    expect(sys.listeners()).toBe(0);
  });

  it("stops listening to the OS when it unmounts", () => {
    const sys = stubSystem(false);
    const { unmount } = mount();
    expect(sys.listeners()).toBe(1);
    unmount();
    expect(sys.listeners()).toBe(0);
  });

  it("works when storage is unavailable: the pick applies, it just is not remembered", () => {
    stubSystem(false);
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new DOMException("denied", "SecurityError");
    });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new DOMException("denied", "SecurityError");
    });
    mount();
    expect(html().dataset.theme).toBe("dark");
    act(() => screen.getByTestId("pick-light").click());
    expect(html().dataset.theme).toBe("light");
  });

  it("follows a choice made in another tab", () => {
    stubSystem(false);
    mount();
    act(() => {
      localStorage.setItem(THEME_STORAGE_KEY, "light");
      window.dispatchEvent(new StorageEvent("storage", { key: THEME_STORAGE_KEY, newValue: "light" }));
    });
    expect(html().dataset.theme).toBe("light");
    expect(screen.getByTestId("choice").textContent).toBe("light");
  });

  it("ignores storage events for other keys", () => {
    stubSystem(false);
    mount();
    act(() => {
      window.dispatchEvent(new StorageEvent("storage", { key: "polyemesis.language", newValue: "fr" }));
    });
    expect(html().dataset.theme).toBe("dark");
  });

  it.each(["/watch", "/watch/abc", "/overlay", "/overlay/dock/k/xyz"])(
    "never themes %s: no data-theme, no listeners, whatever is stored",
    (path) => {
      const sys = stubSystem(true);
      localStorage.setItem(THEME_STORAGE_KEY, "light");
      history.replaceState(null, "", path);
      mount();
      expect(html().hasAttribute("data-theme")).toBe(false);
      expect(sys.listeners()).toBe(0);
    },
  );

  it("does not throw when the browser has no matchMedia", () => {
    vi.stubGlobal("matchMedia", undefined);
    mount();
    expect(html().dataset.theme).toBe("dark");
  });
});
