import { readFileSync } from "node:fs";

/* docs/media/shots.json, as the capture reads it. Read lazily, at the call, so
 * importing this costs nothing and a missing manifest fails the shot that
 * needed it, naming the file. */
const MANIFEST = "../docs/media/shots.json";

interface ShotEntry {
  file: string;
  scene: "sports" | "full" | "lanes";
  test: string;
  allowFailedWords?: string[];
}

function readManifest(): { shots: ShotEntry[] } {
  return JSON.parse(readFileSync(MANIFEST, "utf8")) as { shots: ShotEntry[] };
}

const escapeRegExp = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, String.raw`\$&`);

/** An anchored alternation of the test titles that shoot this scene's
 *  screenshots, for the capture config's `grep`. Several shots share a test, so
 *  titles are deduplicated. Playwright matches against the title path joined
 *  with single spaces ("capture capture.spec.ts <title>", checked with
 *  `playwright test --list --grep`), so the pattern starts at the spec file's
 *  name: a test whose title merely ENDS with a manifest title does not also
 *  run. */
export function titlesFor(scene: "full" | "sports"): RegExp {
  const titles = new Set(readManifest().shots.filter((s) => s.scene === scene).map((s) => s.test));
  if (titles.size === 0) throw new Error(`docs/media/shots.json has no shots for the ${scene} scene`);
  return new RegExp(String.raw`capture\.spec\.ts (?:${[...titles].map(escapeRegExp).join("|")})$`);
}

/** The failed-state words this shot declares as its subject. A shot with no
 *  manifest entry is refused: a screenshot nobody listed is one nobody decided
 *  to publish. */
export function allowedFailedWords(file: string): string[] {
  const manifest = readManifest();
  const entry = manifest.shots.find((s) => s.file === file);
  if (!entry) throw new Error(`${file} has no entry in docs/media/shots.json`);
  return entry.allowFailedWords ?? [];
}
