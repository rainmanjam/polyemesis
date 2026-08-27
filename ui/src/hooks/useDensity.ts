import { useCallback, useEffect, useState } from "react";

/** Where the preference lives. Namespaced like `polyemesis.nav.collapsed`. */
const STORAGE_KEY = "polyemesis.density";

export type Density = "comfortable" | "compact";

/** Reads the stored preference, defaulting to comfortable.
 *
 *  Wrapped because Safari in private mode throws on ANY localStorage access —
 *  see the same guard and the same reasoning in hooks/useNavCollapsed.ts. A
 *  layout preference is not worth breaking the console over, and comfortable is
 *  a working default.
 */
function readStored(): Density {
  try {
    return localStorage.getItem(STORAGE_KEY) === "compact" ? "compact" : "comfortable";
  } catch {
    return "comfortable";
  }
}

/** How tightly the console packs its content, and a toggle for it.
 *
 *  A two-destination install and a thirteen-destination install are not the
 *  same screen, and there is no single spacing that serves both: comfortable
 *  wastes half the viewport on the second, compact is needlessly cramped on the
 *  first. So it is the operator's call, and it is remembered — a density that
 *  resets on reload is a setting nobody uses twice.
 *
 *  The state is written to `document.documentElement` rather than passed down
 *  as a prop, for two reasons that both come from the same place: every page in
 *  this app spaces itself with Tailwind utilities, and Tailwind v4 compiles all
 *  of them to `calc(var(--spacing) * n)`. Rescaling that one variable is the
 *  whole feature; threading a prop through twenty pages to reach the same
 *  effect would also mean twenty places for the two modes to disagree.
 *  index.css scopes the override to <main>, so the chrome and any Radix portal
 *  keep their comfortable geometry — see the DENSITY block there.
 */
export function useDensity(): [Density, () => void] {
  const [density, setDensity] = useState<Density>(readStored);

  useEffect(() => {
    // The attribute, not a class: it is a mode with named values rather than a
    // flag, and index.css selects on it that way.
    document.documentElement.dataset.density = density;
    try {
      localStorage.setItem(STORAGE_KEY, density);
    } catch {
      // Nothing to do and nothing to report: the preference simply does not
      // survive this session, which is the documented Safari behaviour.
    }
  }, [density]);

  const toggle = useCallback(
    () => setDensity((d) => (d === "compact" ? "comfortable" : "compact")),
    [],
  );
  return [density, toggle];
}
