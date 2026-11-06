import { useSyncExternalStore } from "react";

import { authorInk } from "@/lib/authorColor";
import { appliedTheme, subscribeAppliedTheme, type Theme } from "@/lib/theme";

/** The theme currently on the page, re-rendering the caller when it changes.
 *
 *  For canvas drawing, which reads a colour once and cannot follow a CSS
 *  variable: depend on this and re-read the palette when it changes. It reads
 *  the attribute rather than the ThemeProvider's state, so it is right however
 *  the attribute was set, and needs no provider above it. The server snapshot is
 *  dark, the theme of every page that sets none. */
export function useAppliedTheme(): Theme {
  return useSyncExternalStore(subscribeAppliedTheme, () => appliedTheme(), () => "dark");
}

/** A platform-chosen author colour, made readable on the current theme. See
 *  lib/authorColor.ts. Undefined stays undefined (the row has its own tone). */
export function useAuthorInk(color: string | undefined): string | undefined {
  const theme = useAppliedTheme();
  return color ? authorInk(color, theme) : undefined;
}
