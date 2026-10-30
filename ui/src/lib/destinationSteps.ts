/* The add-destination dialog's steps (A6), and which one a save error belongs
 * to.
 *
 * The dialog used to be one column of about thirty fields. It is now four
 * steps over the same form state: the platform, how to connect to it, the
 * audio encode, and video with everything advanced. A refusal from the server
 * arrives as one sentence about one field, and an operator on step 4 cannot
 * fix a stream key they cannot see, so the dialog moves to the step the
 * sentence is about. Matching is on the server's own words; anything not
 * recognised stays where the operator is, with the sentence shown. */

export type DestStep = 1 | 2 | 3 | 4;

export const DEST_STEPS: readonly DestStep[] = [1, 2, 3, 4];

const RULES: readonly [RegExp, DestStep][] = [
  [/\b(name|programme|source)\b/i, 1],
  [/\b(url|stream ?key|key|account|ingest|transport|srt|rtmp|passphrase|latency)\b/i, 2],
  [/\b(audio|bitrate|sample ?rate|channels?|codec|copy)\b/i, 3],
  [/\b(rendition|video|resolution|frame ?rate|compliance|facebook|twitch|tags?|category|backup|multitrack)\b/i, 4],
];

/** The step a server refusal is about, or null when it names no field. */
export function stepForError(message: string): DestStep | null {
  for (const [re, step] of RULES) if (re.test(message)) return step;
  return null;
}
