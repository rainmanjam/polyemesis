/// <reference types="node" />
import { describe, expect, it } from "vitest";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import { fileURLToPath } from "node:url";

/* SENTENCES AN OPERATOR READS, KEPT IN THE CATALOGUE.
 *
 * A run of every route in every locale found about thirty strings still
 * rendering in English whatever the language setting said -- some of them with
 * a translated key sitting unused in all fifteen catalogues ("New rendition",
 * "Loudness target", the chat-retention paragraph). Each has moved to a key.
 * This pins them: a fragment of each English sentence may appear in the
 * catalogues and in comments, and nowhere else in src/.
 *
 * It is a list, not a detector. A general "no English in JSX" rule over this
 * codebase finds hundreds of labels nobody has keyed yet, and a guard that
 * fails on the first day is one that gets switched off. What it does make
 * impossible is these particular strings quietly going back to literals.
 *
 * DELIBERATELY NOT KEYED, and why:
 *   - lib/trackSignal.ts's "waiting for stream" and its two siblings: the
 *     meter-row status words of components/signature, a family that is not
 *     keyed as a whole. Keying one of three words in a row would give a row
 *     that is two-thirds English; the family moves together or not at all.
 *   - "Waiting for the first look at the incoming stream…" is the ENGINE's
 *     status sentence (internal/engine), sent over the wire, not a UI string.
 *   - "Flag harassment, threats, slurs…" is the model's default instruction: a
 *     settings VALUE the operator edits, sent to the model verbatim. Rendering
 *     a translation of it would show the operator a prompt that is not the one
 *     being sent. */

const FRAGMENTS = [
  // AutomodMatrix
  "What each checker may do, on each platform",
  "Off stops every automatic action everywhere",
  "An irreversible action is armed",
  "recorded for review, never acted on",
  "is all flagging does",
  // PlayoutPage
  "A viewer is counted while they keep pulling segments",
  "Playout is off. Nothing is being served",
  "Serve viewers who are not signed in",
  "Lets a player embedded on another website",
  // RoutingPage
  "A whole second mix of the same ingest",
  "One audio mix (the normal case)",
  "Second (VOD) audio mix",
  "Video is copied without re-encoding",
  "to let the audio run ahead",
  "Loudness target",
  // SettingsPage
  "Both apply and the more generous one wins",
  "An inventory declared here does reach Twitch",
  "Enhanced Broadcasting hardware (Twitch)",
  "What this machine's GPU is",
  // ChatPanel
  "Chat is running but no platform account is attached",
  "then restart to attach it",
  "Connected and waiting. Nothing has been said yet",
  // MonitoringPage
  "Choose a destination to edit its command line",
  "No log lines yet",
  "sends to a consumer that had not bound its port",
  // MetersPage and TrackRows
  "Integrated loudness is the figure a platform normalizes",
  "No stream is arriving. Point your encoder",
  "No stream is arriving yet, so this shows the default",
  // RenditionsPage
  "New rendition",
  "copies every audio track through it untouched",
  // AutomationPage
  "No alert rules. Add one with",
  // ClipsPage
  "The buffer is off. Turn it on",
  "costs memory proportional to the stream",
  "Retention keeps at most",
  // PreviewPlayer
  "Start your encoder to see the preview",
  // Destructive confirmations (A18): the title is built from the name in a
  // template literal, which is the one way an English title slips past the
  // catalogue unnoticed. Keyed as confirm.deleteTitle with {name}.
  "Delete “",
  // Dashboard
  "Starts are spread out rather than fired together",
  "This FFmpeg build has no SRT support",
  // MediaUploads
  "for the default source",
  // A19 sweep: the destructive rendition dialog's consequence copy and the
  // Dashboard's results placeholder, both on operator-facing paths.
  "will restart immediately on the source video",
  "Results appear here, one row per platform",
];

/* Bare single-word labels cannot be fragments: "Cancel" is a substring of
 * "onCancel", so a substring hit would be a false positive on every handler.
 * They are matched as exact JSX text instead, between tags, so a prop or an
 * identifier never matches. Restricted to the two directories that are fully
 * keyed; components/signature is the unkeyed family (see trackSignal.ts). */
const JSX_LABELS = ["Cancel", "Save", "Create", "Delete rendition", "Out", "Go live"];

function jsxLabelHits(body: string): string[] {
  return JSX_LABELS.filter((label) => {
    const esc = label.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    return new RegExp(`>\\s*${esc}\\s*<`).test(body);
  });
}

const SRC = fileURLToPath(new URL("..", import.meta.url));

function sources(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) return name === "i18n" ? [] : sources(path);
    if (!/\.(ts|tsx)$/.test(name) || /\.test\.(ts|tsx)$/.test(name)) return [];
    return [path];
  });
}

/** The file with comments removed, so a comment QUOTING a sentence (this repo
 *  writes a lot of those) is not a hit. Line comments only when they start a
 *  line or follow whitespace, so a URL's `//` survives. */
function code(text: string): string {
  return text
    .replace(/\/\*[\s\S]*?\*\//g, "")
    .replace(/(^|\s)\/\/.*$/gm, "$1")
    // JSX wraps prose across lines; collapse so a fragment split by a line
    // break is still found.
    .replace(/\s+/g, " ");
}

describe("operator-facing sentences", () => {
  it("come from the catalogue, not from literals", () => {
    const hits: string[] = [];
    for (const file of sources(SRC)) {
      const body = code(readFileSync(file, "utf8"));
      for (const f of FRAGMENTS) {
        if (body.includes(f)) hits.push(`${relative(SRC, file)}: ${f}`);
      }
    }
    expect(hits).toEqual([]);
  });

  it("keeps bare labels out of JSX text in pages/ and components/", () => {
    const hits: string[] = [];
    for (const file of sources(SRC)) {
      const rel = relative(SRC, file);
      if (!/^(pages|components)\//.test(rel) || rel.startsWith("components/signature/")) continue;
      const body = code(readFileSync(file, "utf8"));
      for (const label of jsxLabelHits(body)) hits.push(`${rel}: ${label}`);
    }
    expect(hits).toEqual([]);
  });
});
