import { useCallback, useState } from "react";

/* ONE SAVE MODEL FOR ANYTHING THAT IS NOT A SWITCH (A95, A20).
 *
 * Five save models lived side by side on the console: Playout saved numbers on
 * blur and toggles at once, Recordings autosaved switches but needed a manual
 * retention save, Clips used Apply, Routing mixed live and pending, and Settings
 * had its own. An operator could not predict what applied.
 *
 * THE RULE, the same one the Settings redesign uses:
 *
 *   - A TOGGLE OR A CHOICE APPLIES AT ONCE, and the page says so. It has no
 *     dirty state, because there is nothing to keep back. Do not use this hook
 *     for it.
 *   - A NUMBER, A TEXT FIELD OR A GROUP OF FIELDS USES AN EXPLICIT SAVE. Hold
 *     the edit in a draft, show "Unsaved changes" while it differs from what is
 *     stored, and show "Saved" once it has been written. Use this hook and
 *     <SaveStatus state={...} /> beside the Save button.
 *
 * Never save on blur. A blur save loses an edit that was never blurred, and it
 * gives no sign that anything was written. */

export type SaveState = "clean" | "dirty" | "saving" | "saved" | "failed";

/** The state a save surface shows, from the three facts it knows. A pending
 *  save wins over everything, then a failure, then the draft against the
 *  stored value, and "saved" only while nothing has been edited since. */
export function saveStateOf(facts: {
  dirty: boolean;
  saving: boolean;
  failed: boolean;
  justSaved: boolean;
}): SaveState {
  if (facts.saving) return "saving";
  if (facts.failed) return "failed";
  if (facts.dirty) return "dirty";
  if (facts.justSaved) return "saved";
  return "clean";
}

export interface SaveModel<T> {
  draft: T;
  setDraft: (next: T) => void;
  dirty: boolean;
  state: SaveState;
  /** Writes the draft. Resolves once the write has settled either way. */
  save: () => Promise<void>;
  /** Drops the draft and returns to the stored value. */
  discard: () => void;
}

/** Holds an editable draft over a stored value. `stored` is what the server
 *  last confirmed. While the draft is clean, a change in `stored` (a refresh, a
 *  reconnect) reseeds it; a dirty draft is never overwritten under the operator. */
export function useSaveModel<T>({
  stored,
  onSave,
  equal = Object.is,
}: {
  stored: T;
  onSave: (draft: T) => Promise<void>;
  equal?: (a: T, b: T) => boolean;
}): SaveModel<T> {
  const [draft, setDraftState] = useState<T>(stored);
  // The last value the server confirmed: the stored value, or the draft that
  // a successful write just put there. "Dirty" means the draft differs from it.
  const [confirmed, setConfirmed] = useState<T>(stored);
  const [saving, setSaving] = useState(false);
  const [failed, setFailed] = useState(false);
  const [justSaved, setJustSaved] = useState(false);

  const dirty = !equal(draft, confirmed);

  // The stored value moved (a refresh, a reconnect). Compare against what was
  // confirmed BEFORE this change, so a clean draft reseeds and a dirty draft is
  // left alone. Done during render, the React-documented way to derive state
  // from a prop change, not in an effect.
  if (!equal(confirmed, stored)) {
    setConfirmed(stored);
    if (!dirty) setDraftState(stored);
  }

  const setDraft = useCallback((next: T) => {
    setDraftState(next);
    setFailed(false);
    setJustSaved(false);
  }, []);

  const save = useCallback(async () => {
    if (saving) return;
    const toWrite = draft;
    setSaving(true);
    setFailed(false);
    try {
      await onSave(toWrite);
      setConfirmed(toWrite);
      setJustSaved(true);
    } catch {
      // The draft stays as it is, so the operator can retry without retyping.
      setFailed(true);
    } finally {
      setSaving(false);
    }
  }, [draft, onSave, saving]);

  const discard = useCallback(() => {
    setDraftState(confirmed);
    setFailed(false);
    setJustSaved(false);
  }, [confirmed]);

  return {
    draft,
    setDraft,
    dirty,
    state: saveStateOf({ dirty, saving, failed, justSaved }),
    save,
    discard,
  };
}

export interface SaveOutcome {
  /** The SaveState to draw for a surface whose draft is (or is not) dirty. */
  stateOf: (dirty: boolean) => SaveState;
  /** Runs one write. The write signals failure by throwing or rejecting; the
   *  error is swallowed here because the caller has already said what went
   *  wrong (a toast carrying the server's sentence) and this only records that
   *  it failed. Resolves once the write has settled either way. */
  run: (write: () => Promise<unknown>) => Promise<void>;
  /** Clears "Saved" and "Couldn't save" once the operator edits again. */
  reset: () => void;
}

/** The saving / failed / saved half of the model for a surface that keeps its
 *  own draft, or has none: a switch that applies at once (the first rule
 *  above), or a form whose dirty test is its own (Settings' Pipeline tab folds
 *  a write-only broker password into it). The state still goes through
 *  saveStateOf, so it is the same SaveState every <SaveStatus> draws, and the
 *  page confirms a save the same way everywhere instead of with a toast on
 *  one page and a status line on the next. */
export function useSaveOutcome(): SaveOutcome {
  const [saving, setSaving] = useState(false);
  const [failed, setFailed] = useState(false);
  const [justSaved, setJustSaved] = useState(false);

  const run = useCallback(async (write: () => Promise<unknown>) => {
    setSaving(true);
    setFailed(false);
    setJustSaved(false);
    try {
      await write();
      setJustSaved(true);
    } catch {
      setFailed(true);
    } finally {
      setSaving(false);
    }
  }, []);

  const reset = useCallback(() => {
    setFailed(false);
    setJustSaved(false);
  }, []);

  const stateOf = useCallback(
    (dirty: boolean) => saveStateOf({ dirty, saving, failed, justSaved }),
    [saving, failed, justSaved],
  );

  return { stateOf, run, reset };
}
