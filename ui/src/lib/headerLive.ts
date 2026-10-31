import type { TranslateParams, TranslationKey } from "@/lib/i18n";

/** What the header's live badge counts, and the words that say whose count it
 *  is (app-council A81).
 *
 *  `status.destinations` is install-wide (LiveDataProvider folds every
 *  programme's frame into it, see mergeStatusDestinations), while the ingest
 *  chip and the programme switcher beside the badge are about the selected
 *  programme. So:
 *
 *    - one programme on the install: there is one scope, and "3 live" is
 *      already unambiguous;
 *    - a programme selected, and its name known: count that programme only and
 *      name it ("Main: 3 live");
 *    - otherwise: count the install and say so ("All programmes: 5 live"),
 *      rather than print a bare number beside a programme-scoped chip. */
export interface HeaderLive {
  count: number;
  key: TranslationKey;
  params: TranslateParams;
}

interface DestLike {
  sourceId?: number | null;
  process?: { state?: string } | null;
}

export function headerLive({
  destinations,
  programme,
  programmes,
  sourceCount,
}: {
  destinations: readonly DestLike[] | undefined;
  programme: number | null;
  programmes: readonly { id: number; name: string }[];
  sourceCount: number;
}): HeaderLive {
  const live = (destinations ?? []).filter((d) => d.process?.state === "running");
  if (sourceCount <= 1) {
    return { count: live.length, key: "chrome.liveCount", params: { count: live.length } };
  }
  const name = programme == null ? undefined : programmes.find((p) => p.id === programme)?.name;
  if (name !== undefined) {
    const count = live.filter((d) => d.sourceId === programme).length;
    return { count, key: "chrome.liveCountProgramme", params: { programme: name, count } };
  }
  return { count: live.length, key: "chrome.liveCountAll", params: { count: live.length } };
}
