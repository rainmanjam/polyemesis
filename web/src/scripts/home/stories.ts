/* Story data and status text for the homepage hero.
 *
 * Three stories share one diagram: four source tracks fan out to destinations,
 * and each destination carries its own mix of them. Everything the hero says
 * about a destination is derived from this table, so the copy cannot drift
 * from the lines the diagram draws.
 *
 * STORIES is deliberately a plain mutable object (not frozen, not `as const`):
 * a test temporarily renames a destination to prove names are rendered as text,
 * then restores it. */

export type StoryKey = "streamer" | "sports" | "worship";
export type Gain = 0 | 0.5 | 1;
/** name is the chip label; phrase is the word used inside sentences. */
export interface Track { name: string; phrase: string }
export interface Dest {
  id: string;
  name: string;
  sub: string;
  mix: [Gain, Gain, Gain, Gain];
  video: boolean;
  archive?: true;
}
export interface Story {
  key: StoryKey;
  tab: string;
  tracks: [Track, Track, Track, Track];
  dests: Dest[];
  caption: string;
  hint: string;
  fine: string;
}

export const STORY_ORDER: readonly StoryKey[] = ["streamer", "sports", "worship"];

const LICENCE = "Use tracks you have the rights to.";
const TRANSLATION =
  "Translation comes from your own interpreters or translation service, as a track you send; polyemesis routes each language.";

export const STORIES: Record<StoryKey, Story> = {
  streamer: {
    key: "streamer",
    tab: "Streamer",
    tracks: [
      { name: "Mic", phrase: "mic" },
      { name: "Music", phrase: "music" },
      { name: "Game", phrase: "game" },
      { name: "Co-host", phrase: "co-host" },
    ],
    dests: [
      { id: "twitch", name: "Twitch", sub: "Full mix", mix: [1, 1, 1, 1], video: true },
      { id: "youtube", name: "YouTube", sub: "No music", mix: [1, 0, 1, 1], video: true },
      { id: "kick", name: "Kick", sub: "Music turned down", mix: [1, 0.5, 1, 1], video: true },
      { id: "archive", name: "Archive", sub: "Every track kept apart", mix: [1, 1, 1, 1], video: true, archive: true },
    ],
    caption: "Twitch gets the full mix. YouTube gets the same video without the music.",
    hint: "Tap a destination to solo its mix.",
    fine: `polyemesis routes audio; it doesn't license music. ${LICENCE}`,
  },
  sports: {
    key: "sports",
    tab: "Sports",
    tracks: [
      { name: "Ambience", phrase: "stadium ambience" },
      { name: "Home", phrase: "home commentary" },
      { name: "Away", phrase: "away commentary" },
      { name: "Spanish", phrase: "Spanish commentary" },
    ],
    dests: [
      { id: "home", name: "Home channel", sub: "Home commentary", mix: [1, 1, 0, 0], video: true },
      { id: "away", name: "Away channel", sub: "Away commentary", mix: [1, 0, 1, 0], video: true },
      { id: "es", name: "Canal en español", sub: "Spanish commentary", mix: [1, 0, 0, 1], video: true },
      { id: "archive", name: "Archive", sub: "All four feeds kept", mix: [1, 1, 1, 1], video: true, archive: true },
    ],
    caption: "One match feed, three commentary channels.",
    hint: "Tap a channel to solo its mix.",
    fine: `${TRANSLATION} It doesn't license content. ${LICENCE}`,
  },
  worship: {
    key: "worship",
    tab: "Worship",
    tracks: [
      { name: "Room", phrase: "the room" },
      { name: "English", phrase: "English" },
      { name: "Spanish", phrase: "Spanish" },
      { name: "Music", phrase: "music" },
    ],
    dests: [
      { id: "en", name: "English service", sub: "English and music", mix: [1, 1, 0, 1], video: true },
      { id: "es", name: "Servicio en español", sub: "Spanish and music", mix: [1, 0, 1, 1], video: true },
      { id: "audio", name: "Audio-only feed", sub: "English, no video", mix: [1, 1, 0, 1], video: false },
      { id: "archive", name: "Archive", sub: "All four tracks kept", mix: [1, 1, 1, 1], video: true, archive: true },
    ],
    caption: "One service, English and Spanish video plus an audio-only feed.",
    hint: "Tap a feed to solo its mix.",
    fine: `${TRANSLATION} It doesn't license music. ${LICENCE}`,
  },
};

/** "a", "a and b", "a, b and c". */
function join(items: string[]): string {
  if (items.length <= 1) return items.join("");
  return `${items.slice(0, -1).join(", ")} and ${items.at(-1)}`;
}

function cap(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1);
}

/** Tracks a destination carries (a lowered one is marked) and the ones it leaves off. */
function split(story: Story, dest: Dest): { carries: string[]; off: string[] } {
  const carries: string[] = [];
  const off: string[] = [];
  story.tracks.forEach((t, i) => {
    const g = dest.mix[i];
    if (g === 0) off.push(t.phrase);
    else carries.push(g === 0.5 ? `${t.phrase} (turned down)` : t.phrase);
  });
  return { carries, off };
}

/** The live status line. With no (or an unknown) destination it is the caption. */
export function statusFor(story: Story, destId: string | null, withHint: boolean): { name: string | null; text: string } {
  const dest = destId === null ? undefined : story.dests.find((d) => d.id === destId);
  if (!dest) return { name: null, text: withHint ? `${story.caption} ${story.hint}` : story.caption };
  const { carries, off } = split(story, dest);
  const parts = [`carries ${join(carries)}.`];
  if (off.length) parts.push(`${cap(join(off))} ${off.length === 1 ? "stays" : "stay"} off.`);
  parts.push(dest.video ? "The video is the same, untouched." : "No video goes to this feed.");
  return { name: dest.name, text: parts.join(" ") };
}

/** Accessible name for a destination button. */
export function destLabel(story: Story, dest: Dest): string {
  const { carries } = split(story, dest);
  const video = dest.video ? ", plus the untouched video" : ", with no video";
  return `${dest.name}: ${dest.sub}. Carries ${join(carries)}${video}. Press to solo this mix.`;
}
