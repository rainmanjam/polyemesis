import { createContext, useCallback, useContext, useEffect, useLayoutEffect, useMemo, useState, type ReactNode } from "react";

import {
  THEME_STORAGE_KEY,
  applyTheme,
  isThemedPath,
  readStoredChoice,
  resolveTheme,
  storeChoice,
  systemPrefersLight,
  type Theme,
  type ThemeChoice,
} from "@/lib/theme";

interface ThemeState {
  /** What the operator picked, or "system" while they have not. */
  choice: ThemeChoice;
  /** What the page is wearing: the choice with "system" resolved. */
  theme: Theme;
  setChoice: (choice: ThemeChoice) => void;
}

/** What a component outside any provider sees: the system choice, and a
 *  setter that does nothing. App mounts the provider around everything, so this
 *  is only reachable from a test that renders one piece of the chrome on its
 *  own -- and for those, a control that renders is worth more than a throw
 *  that makes every AppLayout test wrap itself in a provider it does not test. */
const DETACHED: ThemeState = { choice: "system", theme: "dark", setChoice: () => {} };

const ThemeContext = createContext<ThemeState>(DETACHED);

/** The live half of the theme. public/theme-init.js made the first paint right;
 *  this keeps it right: it applies the resolved theme to <html>, follows the
 *  operating system while the choice is "system", follows a choice made in
 *  another tab, and remembers a pick (lib/theme.ts has the rules).
 *
 *  It is INERT on the public player and the OBS overlay pages: no attribute, no
 *  listeners, whatever is stored. They are never themed. Decided once, because
 *  a viewer never navigates out of /watch and the console never navigates into
 *  it without a reload (the same assumption App.tsx makes). */
export function ThemeProvider({ children }: { children: ReactNode }) {
  const [themed] = useState(() => isThemedPath(window.location.pathname, window.location.hash));
  const [choice, setChoiceState] = useState<ThemeChoice>(readStoredChoice);
  const [prefersLight, setPrefersLight] = useState(systemPrefersLight);

  const theme: Theme = themed ? resolveTheme(choice, prefersLight) : "dark";

  // Layout, not passive: a pick must reach the DOM before the browser paints,
  // or the old theme shows for a frame under the new control.
  useLayoutEffect(() => {
    if (themed) applyTheme(theme);
  }, [themed, theme]);

  // Only while following the system: an explicit pick has no use for the OS's
  // opinion, so it does not listen to it.
  useEffect(() => {
    if (!themed || choice !== "system" || typeof window.matchMedia !== "function") return;
    const query = window.matchMedia("(prefers-color-scheme: light)");
    const onChange = (e: { matches: boolean }) => setPrefersLight(e.matches);
    // The OS may have changed between the first read and this subscription.
    setPrefersLight(query.matches);
    query.addEventListener("change", onChange);
    return () => query.removeEventListener("change", onChange);
  }, [themed, choice]);

  // Another tab picked a theme: this one follows, so two console tabs never
  // disagree about what the operator chose.
  useEffect(() => {
    if (!themed) return;
    const onStorage = (e: StorageEvent) => {
      if (e.key === THEME_STORAGE_KEY) setChoiceState(readStoredChoice());
    };
    window.addEventListener("storage", onStorage);
    return () => window.removeEventListener("storage", onStorage);
  }, [themed]);

  const setChoice = useCallback((next: ThemeChoice) => {
    storeChoice(next);
    setChoiceState(next);
  }, []);

  const value = useMemo(() => ({ choice, theme, setChoice }), [choice, theme, setChoice]);
  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>;
}

export function useTheme(): ThemeState {
  return useContext(ThemeContext);
}
