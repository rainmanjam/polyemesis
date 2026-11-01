// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

import { CaptionPolicyEditor } from "./CaptionPolicyEditor";
import { captionWords } from "@/lib/captions";
import en from "@/lib/i18n/en.json";

// The #8 caption words editor on a programme: its custom vocabulary and its
// masked words, saved together with one button.

afterEach(() => cleanup());

describe("captionWords", () => {
  it("splits on lines and commas and drops blanks", () => {
    expect(captionWords(" Polyemesis \n\nOBS, 4K ,")).toEqual(["Polyemesis", "OBS", "4K"]);
  });
});

describe("CaptionPolicyEditor", () => {
  // MUTATION: Save sends the stored policy instead of the edited lists.
  // Observed: FAIL, the new masked word was not sent.
  it("shows the stored lists and saves both as edited", () => {
    const onSave = vi.fn();
    render(
      <CaptionPolicyEditor
        sourceId={3}
        policy={{ vocabulary: ["Polyemesis"], blocklist: ["darn"] }}
        busy={false}
        onSave={onSave}
      />,
    );
    const vocab = screen.getByLabelText(en["wave3.captions.vocabLabel"]) as HTMLTextAreaElement;
    const block = screen.getByLabelText(en["wave3.captions.blockLabel"]) as HTMLTextAreaElement;
    expect(vocab.value).toBe("Polyemesis");
    expect(block.value).toBe("darn");
    fireEvent.change(block, { target: { value: "darn\nheck" } });
    fireEvent.click(screen.getByRole("button", { name: en["wave3.captions.policySave"] }));
    expect(onSave).toHaveBeenCalledWith({ vocabulary: ["Polyemesis"], blocklist: ["darn", "heck"] });
  });

  it("says what each list does, including that the vocabulary cannot add a word the recogniser missed", () => {
    render(<CaptionPolicyEditor sourceId={3} busy={false} onSave={() => {}} />);
    expect(screen.getByText(en["wave3.captions.vocabHint"])).toBeTruthy();
    expect(screen.getByText(en["wave3.captions.blockHint"])).toBeTruthy();
  });
});
