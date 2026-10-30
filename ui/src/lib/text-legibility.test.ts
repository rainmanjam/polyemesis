import { describe, expect, it } from "vitest";
import { readFileSync, readdirSync } from "node:fs";
import { join, relative } from "node:path";

/* Legibility guards for the foundation stream (B2).
 *
 * --subtle-foreground is about 3.1:1 against --card, under the 4.5:1 WCAG AA
 * floor for small text. WCAG exempts disabled controls, and placeholder text is
 * the convention this app follows, so the token is reserved for exactly those
 * two things. Every other place that used it for words -- off-state chips,
 * helper lines, inactive tabs -- moves to --muted-foreground (about 5.8:1).
 *
 * The guard is line-based on purpose: the rule is about the class that paints
 * the text, and a line that names `disabled` or `placeholder` is the only
 * exemption the app recognises. A new enabled control that reaches for the
 * token fails here, not in a contrast review months later. */

const SRC = new URL("../", import.meta.url).pathname; // ui/src/

function sourceFiles(dir: string): string[] {
  return readdirSync(dir, { recursive: true, withFileTypes: false })
    .map((name) => join(dir, String(name)))
    .filter((p) => /\.(tsx|ts)$/.test(p) && !/\.test\.(tsx|ts)$/.test(p));
}

// A usage is exempt when it is itself scoped to a disabled or placeholder
// state (`disabled:text-subtle-foreground`, `placeholder:…`), or the line
// applies it under a disabled condition. A line that merely MENTIONS a
// disabled: variant somewhere else no longer exempts an enabled colour beside
// it, which is how a hover-revealed delete button kept 3.1:1 text (B2).
const EXEMPT_PREFIX = /(disabled|placeholder)[\w-]*:(?:[\w-]+:)*$/;
const EXEMPT_LINE = /\b(disabled|isDisabled|aria-disabled)\b(?!:)/;
const EXEMPT = { test: (line: string) => usagesExempt(line) };

function usagesExempt(line: string): boolean {
  const re = /(\S*?)text-subtle-foreground\b/g;
  let m: RegExpExecArray | null;
  let any = false;
  while ((m = re.exec(line))) {
    any = true;
    if (!EXEMPT_PREFIX.test(m[1]) && !EXEMPT_LINE.test(line)) return false;
  }
  return any;
}

describe("text-subtle-foreground is reserved for disabled and placeholder text", () => {
  const files = sourceFiles(SRC);

  it("the scanner reaches the sources and finds the exempt usages it must allow", () => {
    // Positive control: if the walk ever stops matching, the guard below passes
    // vacuously. Exempt usages exist today, so zero here means a broken scanner.
    let exempt = 0;
    for (const f of files) {
      for (const line of readFileSync(f, "utf8").split("\n")) {
        if (/\btext-subtle-foreground\b/.test(line) && EXEMPT.test(line)) exempt++;
      }
    }
    expect(files.length).toBeGreaterThan(100);
    expect(exempt).toBeGreaterThan(0);
  });

  it("no enabled text paints with text-subtle-foreground", () => {
    const hits: string[] = [];
    for (const f of files) {
      readFileSync(f, "utf8")
        .split("\n")
        .forEach((line, i) => {
          if (/\btext-subtle-foreground\b/.test(line) && !EXEMPT.test(line)) {
            hits.push(`${relative(SRC, f)}:${i + 1}`);
          }
        });
    }
    expect(hits, "use text-muted-foreground for enabled text").toEqual([]);
  });
});

/* THE TYPE FLOOR ON CONFIGURATION SURFACES.
 *
 * Labels and values on the pages where an operator types a destination, a
 * rendition or a routing rule were 9-11px. The floor there is --text-sm (12px),
 * the console's own default. Badges keep the 10px badge step: they are read
 * as tags, not as values, and the Badge component owns that size. */
const CONFIG_SURFACES = [
  "components/DestinationDialog.tsx",
  "pages/RenditionsPage.tsx",
  "pages/RoutingPage.tsx",
  "components/AutomodConfig.tsx",
  "components/signature/TrackRows.tsx",
  "components/signature/MixMatrix.tsx",
  // The rest of the places an operator configures something (B2).
  "components/ChatRules.tsx",
  "components/AutomodMatrix.tsx",
  "components/PullIngestFields.tsx",
  "components/PlaylistEditor.tsx",
  "components/DeviceCodeDialog.tsx",
  "pages/PlayoutPage.tsx",
  "components/DebugSettings.tsx",
  "components/ConfirmDestructive.tsx",
  "components/DangerZone.tsx",
  "components/PresetReviewDialog.tsx",
  "components/SetupChecklist.tsx",
  "components/signature/RoutingSaveBar.tsx",
  "pages/SettingsPage.tsx",
  "pages/SourcesPage.tsx",
  "components/HooksCard.tsx",
  "pages/JobsPage.tsx",
  "pages/AutomationPage.tsx",
  "components/MediaUploads.tsx",
];

describe("config surfaces hold a 12px floor for labels and values", () => {
  it.each(CONFIG_SURFACES)("%s uses no text smaller than 12px", (rel) => {
    const src = readFileSync(join(SRC, rel), "utf8");
    const small = src.match(/text-\[(9|10|11)px\]|\btext-(micro|tiny)\b/g) ?? [];
    expect(small, `${rel} still sets text below 12px`).toEqual([]);
  });
});
