import { useCallback, useState } from "react";
import type { TranslationKey } from "./i18n";
import type { Settings } from "./types";

/* ONE "SHOW ADVANCED" SWITCH FOR THE SETTINGS PAGE (settings-council R4, R7).
 *
 * Expert cards -- MQTT, the preview encoder, meter cadence, process-log files,
 * Enhanced Broadcasting hardware -- used to sit at the same weight as the
 * three settings most operators ever change. They now sit in one Advanced
 * group per page, hidden until the operator turns the switch on.
 *
 * HIDDEN, NEVER UNMOUNTED. The cards stay in the DOM with `hidden`, which keeps
 * three things working that an unmounted card would break: the settings
 * reachability walk that proves every leaf has a control, the search's reveal
 * (it focuses a control by id), and the single Save over the tab's one draft.
 *
 * AND NEVER HIDING SOMETHING THE OPERATOR NEEDS TO SEE. A card whose values
 * differ from the shipped defaults, or one the last save complained about, is
 * shown whatever the switch says: hiding a non-default value is how an install
 * comes to behave in a way nobody on screen can explain. */

/** Card titles (their catalogue keys) that count as advanced. The search index
 *  uses the same keys, which is what lets a hit on one of them open it. */
export const ADVANCED_CARDS = [
  "set.mqtt",
  "set.preview",
  "set.audioMeters",
  "set.processLogs",
  "set.ebTitle",
] as const satisfies readonly TranslationKey[];

export type AdvancedCard = (typeof ADVANCED_CARDS)[number];

export function isAdvancedCard(key: string): key is AdvancedCard {
  return (ADVANCED_CARDS as readonly string[]).includes(key);
}

/** Whether a card shows a value the operator (or an older release) chose, as
 *  against what db.DefaultSettings ships. The defaults are written out here
 *  rather than imported because the server owns them; settingsAdvanced.test.ts
 *  pins each one, so a changed default fails a test instead of hiding a card
 *  that should show. */
export function advancedCardIsNonDefault(card: AdvancedCard, s: Settings): boolean {
  switch (card) {
    case "set.mqtt":
      // Off with nothing typed is the default. Anything else is a broker the
      // operator configured, enabled or not.
      return (s.mqtt?.enabled ?? false) || (s.mqtt?.brokerUrl ?? "") !== "";
    case "set.preview":
      return (
        s.preview.enabled !== true ||
        s.preview.segmentSeconds !== 1 ||
        s.preview.videoHeight !== 360 ||
        s.preview.videoKbps !== 800 ||
        s.preview.idleTimeoutSeconds !== 30
      );
    case "set.audioMeters":
      return s.meters.enabled !== true || s.meters.intervalMs !== 100;
    case "set.processLogs":
      return (
        s.logging.persistProcessLogs !== true || s.logging.maxFileMb !== 8 || s.logging.maxFiles !== 3
      );
    case "set.ebTitle":
      return (s.multitrack?.gpus?.length ?? 0) > 0;
  }
}

/** Whether a card is drawn. `revealed` holds cards a search result opened. */
export function advancedCardShown(
  card: AdvancedCard,
  input: { show: boolean; settings: Settings; inError: boolean; revealed: ReadonlySet<string> },
): boolean {
  return (
    input.show ||
    input.inError ||
    input.revealed.has(card) ||
    advancedCardIsNonDefault(card, input.settings)
  );
}

const STORAGE_KEY = "polyemesis.settings.showAdvanced";

function readStored(): boolean {
  try {
    return window.localStorage.getItem(STORAGE_KEY) === "1";
  } catch {
    // Private-mode Safari throws on any access; off is the safe answer.
    return false;
  }
}

/** The switch, remembered per browser. Off by default: the short page is the
 *  one a first-time operator should meet. */
export function useShowAdvanced(): [boolean, (next: boolean) => void] {
  const [show, setShow] = useState(readStored);
  const update = useCallback((next: boolean) => {
    setShow(next);
    try {
      window.localStorage.setItem(STORAGE_KEY, next ? "1" : "0");
    } catch {
      // Not remembered, still applied for this visit.
    }
  }, []);
  return [show, update];
}
