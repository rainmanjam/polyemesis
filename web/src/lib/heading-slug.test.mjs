import { describe, expect, it } from "vitest";

import { slugify, uniqueSlugger } from "./heading-slug.mjs";

describe("slugify (GitHub's rule)", () => {
  it("lower-cases and joins words with hyphens", () => {
    expect(slugify("Two mixes to one destination")).toBe("two-mixes-to-one-destination");
    expect(slugify("It needs a GPU")).toBe("it-needs-a-gpu");
  });

  it("drops punctuation without touching the words around it", () => {
    expect(slugify("On Twitch, vodProfile needs multitrack")).toBe("on-twitch-vodprofile-needs-multitrack");
  });

  it("does not collapse the hyphens a dropped character leaves behind", () => {
    expect(slugify("Install (Docker) & more")).toBe("install-docker--more");
  });

  it("keeps hyphens, underscores and digits", () => {
    expect(slugify("The -reset-admin flag_2")).toBe("the--reset-admin-flag_2");
  });

  it("keeps letters outside ASCII", () => {
    expect(slugify("Español Über")).toBe("español-über");
  });

  it("returns an empty string for text with nothing to keep", () => {
    expect(slugify("()")).toBe("");
    expect(slugify("")).toBe("");
  });
});

describe("uniqueSlugger", () => {
  it("numbers repeats from -1", () => {
    const slug = uniqueSlugger();
    expect(slug("Overview")).toBe("overview");
    expect(slug("Overview")).toBe("overview-1");
    expect(slug("Overview")).toBe("overview-2");
  });

  it("counts each base slug on its own", () => {
    const slug = uniqueSlugger();
    expect(slug("Notes")).toBe("notes");
    expect(slug("Other")).toBe("other");
    expect(slug("Notes")).toBe("notes-1");
  });

  it("does not hand out a slug a heading really has", () => {
    // "Notes 1" really exists, so the second "Notes" must not collide with it.
    const slug = uniqueSlugger();
    expect(slug("Notes")).toBe("notes");
    expect(slug("Notes-1")).toBe("notes-1");
    expect(slug("Notes")).toBe("notes-2");
  });

  it("starts clean for every slugger", () => {
    uniqueSlugger()("Overview");
    expect(uniqueSlugger()("Overview")).toBe("overview");
  });
});
