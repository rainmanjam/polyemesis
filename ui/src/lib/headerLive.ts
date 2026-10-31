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
  /** What to say when `count` is 0: the programme's name with "Off air" when
   *  the count is scoped to one, so the phone's on-air line does not say a
   *  bare "Off air" while another programme is broadcasting. */
  offKey: TranslationKey;
  offParams?: TranslateParams;
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
    return { count: live.length, key: "chrome.liveCount", params: { count: live.length }, offKey: "onAir.offAir" };
  }
  const name = programme == null ? undefined : programmes.find((p) => p.id === programme)?.name;
  if (name !== undefined) {
    const count = live.filter((d) => d.sourceId === programme).length;
    return {
      count,
      key: "chrome.liveCountProgramme",
      params: { programme: name, count },
      offKey: "chrome.offAirProgramme",
      offParams: { programme: name },
    };
  }
  return { count: live.length, key: "chrome.liveCountAll", params: { count: live.length }, offKey: "onAir.offAir" };
}
