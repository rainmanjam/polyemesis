/* Platform names, from the server's registry.
 *
 * internal/platform is the one table of per-platform facts. The console used to
 * keep six maps of display names (audience, chat accent, overlay settings, the
 * destination card, the chat menu, the poll panel), each missing a different
 * platform. They all read this now, and the registry reaches the console as
 * contract/wire.generated.ts (the same description is served at
 * GET /api/v1/platforms/registry), so a platform added to the registry is named
 * everywhere without anybody editing the console. */

import { WIRE } from "@/lib/contract/wire";

const NAME_BY_ENUM: ReadonlyMap<string, string> = new Map(
  WIRE.platforms.platforms.flatMap((p) => ("enum" in p ? [[p.enum, p.shortName] as const] : [])),
);

/** The name a platform reads as in a sentence or beside a chat line ("YouTube",
 *  "X"). Retired platforms are named too: a row saved before the shutdown still
 *  carries one. An id the registry does not know is shown as it came, not
 *  hidden. */
export function platformName(platform: string): string {
  return NAME_BY_ENUM.get(platform) ?? platform;
}

/** The longest go-live title X accepts, in characters. X documents the publish
 *  title as "subject to X post length limits" with no number, so the post limit
 *  is used; the server enforces the same bound. */
export const X_GO_LIVE_TITLE_MAX: number = WIRE.platforms.rules.xGoLiveTitleMax;
