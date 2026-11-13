/* The latest release, read from CHANGELOG.md at build time. The changelog is
 * in every build (the Docker build copies it from the repo root), unlike git
 * tags, which a shallow clone does not have. Nothing is typed here: a null
 * result makes the footer say nothing rather than guess. */
import { readFileSync } from "node:fs";
import { resolve } from "node:path";

export interface Release {
  /** "0.18.0" */
  version: string;
  /** "2026-11-11" */
  date: string;
}

/* `## [0.18.0] — 2026-11-11`; a hyphen is accepted for the em dash. The
 * `## [Unreleased]` heading has no version and never matches. */
const HEADING = /^## \[(\d+\.\d+\.\d+)\]\s+[—-]\s+(\d{4}-\d{2}-\d{2})\s*$/m;

export function parseLatestRelease(changelog: string): Release | null {
  const match = HEADING.exec(changelog);
  return match ? { version: match[1], date: match[2] } : null;
}

/* The build runs with web/ as its working directory, so the changelog is one
 * level up. Returns null if it cannot be read. */
export function latestRelease(): Release | null {
  try {
    return parseLatestRelease(readFileSync(resolve(process.cwd(), "..", "CHANGELOG.md"), "utf8"));
  } catch {
    return null;
  }
}

/* `v0.18.0, released 2026-11-11`: how a page states maturity, factually. */
export function releaseLine(r: Release | null): string | null {
  return r ? `v${r.version}, released ${r.date}` : null;
}

/* Replace every `{release}` token in page copy with the release line, or with
 * a neutral phrase when the changelog could not be read. */
export function fillRelease(text: string, r: Release | null): string {
  return text.replaceAll("{release}", releaseLine(r) ?? "the current release");
}
