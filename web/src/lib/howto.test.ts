import { describe, expect, it } from "vitest";

import { RECIPES } from "../data/recipes.mjs";
import { howToLd } from "./howto";

const first = RECIPES[0];
const ld = howToLd(first) as {
  "@context": string;
  "@type": string;
  name: string;
  description: string;
  tool: { "@type": string; name: string }[];
  supply: { "@type": string; name: string }[];
  step: { "@type": string; name: string; text: string; url: string }[];
};

describe("howToLd", () => {
  it("describes the recipe as a HowTo", () => {
    expect(ld["@context"]).toBe("https://schema.org");
    expect(ld["@type"]).toBe("HowTo");
    expect(ld.name).toBe("Send two languages from one broadcast");
    expect(ld.description).toBe(first.description);
    expect(ld.tool).toEqual([{ "@type": "HowToTool", name: "OBS Studio" }]);
    expect(ld.supply).toEqual([{ "@type": "HowToSupply", name: "A running polyemesis server" }]);
  });

  it("has one HowToStep per step, named without the trailing period and linked to its anchor", () => {
    expect(ld.step).toHaveLength(4);
    for (const s of ld.step) expect(s["@type"]).toBe("HowToStep");
    expect(ld.step[0].name).toBe("Give each voice its own track");
    expect(ld.step[0].url).toBe("https://polyemesis.com/recipes/two-languages-one-broadcast#step-1");
    expect(ld.step[3].url).toBe("https://polyemesis.com/recipes/two-languages-one-broadcast#step-4");
    for (const s of ld.step) expect(s.name).not.toMatch(/\.$/);
  });

  it("joins a step's facts with a space", () => {
    const facts = first.steps[1].facts.map((f: { text: string }) => f.text);
    expect(facts).toHaveLength(2);
    expect(ld.step[1].text).toBe(facts.join(" "));
  });

  it("takes the site from its second argument", () => {
    expect((howToLd(first, "https://example.test") as typeof ld).step[0].url).toBe(
      "https://example.test/recipes/two-languages-one-broadcast#step-1",
    );
  });

  it("carries no totalTime or video anywhere", () => {
    for (const r of RECIPES) expect(JSON.stringify(howToLd(r))).not.toMatch(/totalTime|"video"/);
  });
});
