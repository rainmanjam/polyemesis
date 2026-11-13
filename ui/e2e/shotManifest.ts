import { readFileSync } from "node:fs";

/* docs/media/shots.json, as the capture reads it. Read lazily, at the call, so
 * importing this costs nothing and a missing manifest fails the shot that
 * needed it, naming the file. */
const MANIFEST = "../docs/media/shots.json";

interface ShotEntry {
  file: string;
  allowFailedWords?: string[];
}

/** The failed-state words this shot declares as its subject. A shot with no
 *  manifest entry is refused: a screenshot nobody listed is one nobody decided
 *  to publish. */
export function allowedFailedWords(file: string): string[] {
  const manifest = JSON.parse(readFileSync(MANIFEST, "utf8")) as { shots: ShotEntry[] };
  const entry = manifest.shots.find((s) => s.file === file);
  if (!entry) throw new Error(`${file} has no entry in docs/media/shots.json`);
  return entry.allowFailedWords ?? [];
}
