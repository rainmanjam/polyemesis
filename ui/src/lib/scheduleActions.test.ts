import { describe, expect, it } from "vitest";

import contract from "./contract/scheduler-actions.json";
import {
  SCHEDULE_ACTIONS,
  SCHEDULE_ACTION_LABEL_KEYS,
  isPlaylistAction,
  isStartAction,
  scheduleActionLabel,
  type ScheduleAction,
} from "./scheduleActions";
import en from "./i18n/en.json";
import { translate } from "./i18n";

/**
 * The UI half of the schedule-action contract.
 *
 * The Go half is internal/scheduler's
 * TestTheCommittedContractMatchesWhatTheServerOffers, which writes
 * contract/scheduler-actions.json from the same slice its Validate ranges over.
 * This side imports the REAL module the dropdown renders from and compares.
 *
 * That import is the point, and it is what issue #107 says the old guard could
 * not do. A Go test grepping AutomationPage.tsx for `<SelectItem value="...">`
 * passes whether or not the file compiles, whether or not the string is inside
 * a comment, and whether or not the component ever renders. This file cannot
 * run at all unless scheduleActions.ts type-checks and loads.
 *
 * Both gates fire on every pull request: the go job runs the Go half, the ui
 * job runs this one. Changing the server's list without the UI's turns the ui
 * job red, and vice versa.
 */
describe("the schedule actions the server offers", () => {
  it("are exactly the ones the dropdown renders", () => {
    // Order included on purpose: the contract is generated in the order the
    // dropdown offers them, and a silent reordering of a select is a real (if
    // small) UI change that should be a deliberate one.
    expect([...SCHEDULE_ACTIONS]).toEqual(contract.actions);
  });

  it("has no action without a label", () => {
    for (const a of SCHEDULE_ACTIONS) {
      const key = SCHEDULE_ACTION_LABEL_KEYS[a];
      expect(key, `no label key for ${a}`).toBeTruthy();
      // The key must exist in the catalogue, not merely be a string. A missing
      // key renders as the key itself, which ships as a dropdown option reading
      // "auto.startPlaylist".
      expect(Object.keys(en), `${key} is not in en.json`).toContain(key);
    }
  });

  it("renders a translated label for every action", () => {
    const t = ((key: string) => translate("en", key as never)) as never;
    for (const a of SCHEDULE_ACTIONS) {
      const label = scheduleActionLabel(a, t);
      expect(label, `${a} has an empty label`).toBeTruthy();
      expect(label, `${a} rendered its own key`).not.toBe(
        SCHEDULE_ACTION_LABEL_KEYS[a],
      );
    }
  });

  /**
   * These two mirror the server's TargetsPlaylist() and Enables(). They are
   * asserted here rather than assumed because the destination path reads
   * isStartAction: route a playlist.stop by it and the UI would show every
   * destination about to be disabled.
   */
  it("routes each action to the same half the server does", () => {
    const playlist: ScheduleAction[] = ["playlist.start", "playlist.stop"];
    const starts: ScheduleAction[] = ["start", "playlist.start"];
    for (const a of SCHEDULE_ACTIONS) {
      expect(isPlaylistAction(a), `isPlaylistAction(${a})`).toBe(
        playlist.includes(a),
      );
      expect(isStartAction(a), `isStartAction(${a})`).toBe(starts.includes(a));
    }
  });
});
