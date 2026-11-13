import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

import { CLAIMS } from "../data/feature-claims.mjs";
import { ALSO } from "../data/features.mjs";

// The Go test (internal/testenv/features_claim_test.go) checks each receipt
// against the console. This file checks that the receipts cover the page:
// every claim the page makes has exactly one, and none is left over.

const here = dirname(fileURLToPath(import.meta.url));
const page = readFileSync(join(here, "..", "pages", "features.astro"), "utf8");

const sectionIds = [...page.matchAll(/<section id="([a-z0-9-]+)"/g)].map((m) => m[1]).filter((id) => id !== "also");
const alsoIds = ALSO.flatMap((g) => g.items.map((i) => `also-${i.slug}`));
const wanted = [...sectionIds, ...alsoIds];
const claimIds = CLAIMS.map((c) => c.id);

describe("the features page claims", () => {
  it("reads the page's sections and the ALSO list", () => {
    expect(sectionIds).toEqual(["audio", "inputs", "failover", "chat", "recordings", "deck"]);
    expect(alsoIds).toHaveLength(18);
  });

  it("gives every ALSO item a distinct slug", () => {
    expect(new Set(alsoIds).size).toBe(alsoIds.length);
  });

  it("has exactly one claim for every section and ALSO item", () => {
    for (const id of wanted) {
      expect(claimIds.filter((c) => c === id), id).toHaveLength(1);
    }
  });

  it("has no claim that names no section or item", () => {
    const orphans = claimIds.filter((id) => !wanted.includes(id));
    expect(orphans).toEqual([]);
  });

  it("has unique ids", () => {
    expect(new Set(claimIds).size).toBe(claimIds.length);
  });

  it("gives each claim exactly one receipt", () => {
    for (const c of CLAIMS) {
      const receipts = ["provenBy", "claimIsBehaviour"].filter((k) => k in c);
      expect(receipts, c.id).toHaveLength(1);
    }
  });

  it("explains every behaviour in at least 30 characters", () => {
    for (const c of CLAIMS) {
      if ("claimIsBehaviour" in c) expect(c.claimIsBehaviour.length, c.id).toBeGreaterThanOrEqual(30);
    }
  });
});
