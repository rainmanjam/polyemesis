import { describe, expect, it } from "vitest";
import { formComplete, formKey, inFlight, isPublishCause, parseTags, tagsText, type PublishForm, type Publication } from "./publish";

const form: PublishForm = {
  clipJobId: 4, platform: "youtube", accountId: 2, title: " Hi ", description: "d", tags: ["a"], privacy: "private",
};

describe("hashtag parsing", () => {
  it("accepts spaces, commas and a leading #, and drops repeats", () => {
    expect(parseTags("fun, #clips  gaming ##fun")).toEqual(["fun", "clips", "gaming"]);
    expect(parseTags("")).toEqual([]);
    expect(parseTags(" , # ")).toEqual([]);
  });
  it("round-trips through the text a person sees", () => {
    expect(parseTags(tagsText(["a", "b"]))).toEqual(["a", "b"]);
  });
});

describe("the form's identity", () => {
  it("changes when any reviewed field changes, so a confirmation cannot outlive an edit", () => {
    const base = formKey(form);
    for (const edit of [
      { title: "Other" }, { description: "x" }, { tags: ["b"] }, { privacy: "public" }, { accountId: 3 }, { platform: "tiktok" as const },
    ]) {
      expect(formKey({ ...form, ...edit })).not.toBe(base);
    }
  });
  it("ignores surrounding whitespace in the title and description", () => {
    expect(formKey({ ...form, title: "Hi" })).toBe(formKey(form));
  });
});

describe("completeness and states", () => {
  it("needs a clip, an account and a title", () => {
    expect(formComplete(form)).toBe(true);
    expect(formComplete({ ...form, title: "  " })).toBe(false);
    expect(formComplete({ ...form, accountId: 0 })).toBe(false);
  });
  it("knows which publications are still running", () => {
    const p = (state: Publication["state"]) => ({ state }) as Publication;
    expect(inFlight(p("confirmed"))).toBe(true);
    expect(inFlight(p("uploading"))).toBe(true);
    expect(inFlight(p("posted"))).toBe(false);
    expect(inFlight(p("failed"))).toBe(false);
    expect(inFlight(null)).toBe(false);
  });
  it("recognises only the causes it has words for", () => {
    expect(isPublishCause("quota_exceeded")).toBe(true);
    expect(isPublishCause("made_up")).toBe(false);
    expect(isPublishCause(undefined)).toBe(false);
  });
});
