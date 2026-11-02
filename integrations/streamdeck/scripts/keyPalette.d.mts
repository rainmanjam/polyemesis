export type Rgb = [number, number, number];
export function oklch(L: number, C: number, hDeg: number): Rgb;
export function luminance(rgb: readonly number[]): number;
export function contrast(a: readonly number[], b: readonly number[]): number;
export const TITLE_INK: Rgb;
export const SHADE_TOP: number;
export const SHADE_SLOPE: number;
export const KEY_COLOURS: Record<string, Rgb>;
