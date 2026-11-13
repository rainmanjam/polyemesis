/* What the hero shows for each case, and how its level meters and clock move.
 * Pure: Hero.astro renders from heroModel on the server, hero.ts animates with
 * meterLevel and clockText. Nothing here is a measurement; the meters and the
 * clock are a picture of the product, so they are deterministic (a function of
 * time and the destination's index), never random. */
import { CASES, CASE_ORDER, mixLabel } from "../../data/cases.mjs";

export type CaseKey = (typeof CASE_ORDER)[number];

export interface Chip {
  label: string;
  on: boolean;
}
export interface Tile {
  name: string;
  city: string;
  mix: string;
  chips: Chip[];
  /** The level the meter rests at when motion is off, 0..1. */
  rest: number;
}
export interface HeroModel {
  flow: string;
  header: { destinations: number; mixes: number };
  tiles: Tile[];
}

/* Where each destination's meter sits at rest, and how far and how fast it
 * swings: different per index so five meters never move together. */
const BASE = [0.62, 0.42, 0.55, 0.3, 0.18];
export const meterRest = (i: number): number => BASE[i % BASE.length];

/** Meter level 0..1 at `t` seconds for destination `i`. */
export function meterLevel(i: number, t: number): number {
  const v =
    meterRest(i) + 0.2 * Math.sin(t * (0.9 + 0.35 * i) + i * 1.7) + 0.07 * Math.sin(t * (2.3 + 0.5 * i) + i);
  return Math.min(1, Math.max(0.05, v));
}

/** The ON AIR clock after `seconds` more seconds than the page's fixed start. */
export const CLOCK_START = 14 * 60 + 35; // 00:14:35
export function clockText(seconds: number): string {
  const total = CLOCK_START + seconds;
  const p = (n: number) => String(n).padStart(2, "0");
  return `ON AIR ${p(Math.floor(total / 3600))}:${p(Math.floor((total % 3600) / 60))}:${p(total % 60)}`;
}

export function heroModel(key: CaseKey): HeroModel {
  const c = CASES[key];
  const mixes = new Set(c.dests.map((d: { tracks: string[] }) => [...d.tracks].sort().join("|")));
  return {
    flow: `1 stream in → 1 video + ${c.tracks.length} audio tracks → ${c.dests.length} destinations`,
    header: { destinations: c.dests.length, mixes: mixes.size },
    tiles: c.dests.map((d: { name: string; city: string; tracks: string[] }, i: number) => ({
      name: d.name,
      city: d.city,
      mix: mixLabel(c, d),
      chips: c.tracks.map((t: { id: string; label: string }) => ({ label: t.label, on: d.tracks.includes(t.id) })),
      rest: meterRest(i),
    })),
  };
}
