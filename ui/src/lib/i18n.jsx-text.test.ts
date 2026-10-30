/// <reference types="node" />
import { describe, expect, it } from "vitest";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import { fileURLToPath } from "node:url";
// Vite's own parser (oxc, through rolldown): the same one that compiles these
// files, so what it calls JSX text is what renders.
import { parseAst } from "rolldown/parseAst";

/* NO BARE ENGLISH IN JSX (A19).
 *
 * i18n.literals.test.ts pins particular sentences. This is the general rule
 * the council asked for: in pages/ and components/, text an operator reads --
 * JSX text, a string child, or a string title / placeholder / aria-label /
 * alt / label / description -- comes from the catalogue. It runs the parser
 * rather than a regex, because a regex over TSX cannot tell `a > b` from a
 * closing tag and would be switched off within a week.
 *
 * ALLOWED is the short list of things that are the same in every language:
 * units, product and protocol names, and literal example values. Anything new
 * that reads as English fails here with its file and line. */

const ALLOWED = new Set<string>([
  // units and symbols
  "ms", "s", "dB", "dBFS", "dBTP", "LU", "LUFS", "kbps", "fps", "Hz", "MP/s", "×", "→", "·",
  // product, protocol and format names
  "polyemesis", "RTMP", "SRT", "HLS", "FFmpeg", "OBS", "MKV", "MP4", "UTC", "API", "rss",
  "Discord", "Slack", "YouTube", "Twitch", "Kick", "Facebook", "mpegts", "ffmpeg", "srt",
  "macOS", "Linux", "Windows", "NVIDIA", "GeForce", "RTX",
  // code identifiers shown as code
  "drawtext", ".ttf", ".otf",
]);

/** Whole strings that are names or code, the same in every language. */
const ALLOWED_TEXT = new Set<string>([
  "Enhanced Broadcasting (Twitch)", // Twitch's product name; every catalogue keeps it
  "-c:v copy", // an FFmpeg argument, shown as code
  "update-ca-certificates", // a command the operator types
]);

/** A string is allowed when, with units, numbers and punctuation removed, what
 *  is left is empty or entirely allowed words. */
function allowed(text: string): boolean {
  if (ALLOWED_TEXT.has(text.trim())) return true;
  // A path, file name, flag or address: no spaces, and punctuation a word lacks.
  if (/^[\w.:/@~-]+$/.test(text.trim()) && /[./:@_~]|^-/.test(text.trim())) return true;
  const words = text
    .replace(/\{[^}]*\}/g, " ")
    .split(/[\s,;:()[\]/+\-–—@]+/)
    .filter((w) => /[A-Za-z]{2,}/.test(w));
  return words.every((w) => ALLOWED.has(w) || ALLOWED.has(w.replace(/[.]+$/, "")));
}

const ATTRS = new Set(["title", "placeholder", "aria-label", "alt", "label", "description"]);

/** Literal example values in placeholders, which are values, not prose. */
const PLACEHOLDER_VALUE = /^(?:[a-z]+:\/\/|-[a-z_]|[A-Za-z0-9]+(?:-[A-Za-z0-9]+){2,}$|0x|https?:|[\w.]+@[\w.]+$)/;

interface Hit {
  line: number;
  text: string;
}

function lineAt(src: string, offset: number): number {
  return src.slice(0, offset).split("\n").length;
}

function scan(src: string): Hit[] {
  const ast = parseAst(src, { lang: "tsx" }) as unknown as { body: unknown[] };
  const hits: Hit[] = [];
  const visit = (node: unknown, parent: Record<string, unknown> | null): void => {
    if (!node || typeof node !== "object") return;
    const n = node as Record<string, unknown> & { type?: string; start?: number };
    if (n.type === "JSXText") {
      const text = String(n.value).replace(/\s+/g, " ").trim();
      if (/[A-Za-z]{2,}/.test(text) && !allowed(text)) hits.push({ line: lineAt(src, n.start ?? 0), text });
    } else if (n.type === "JSXAttribute") {
      const name = (n.name as { name?: string })?.name ?? "";
      const value = n.value as { type?: string; value?: unknown } | null;
      if (ATTRS.has(name) && value?.type === "Literal" && typeof value.value === "string") {
        const text = value.value;
        // `title="ns.key"` is InfoHint taking a catalogue key, not prose.
        const isKey = /^[a-zA-Z0-9]+(\.[a-zA-Z0-9]+)+$/.test(text);
        const isValue = name === "placeholder" && PLACEHOLDER_VALUE.test(text);
        if (/[A-Za-z]{2,}/.test(text) && !isKey && !isValue && !allowed(text)) {
          hits.push({ line: lineAt(src, n.start ?? 0), text: `${name}="${text}"` });
        }
      }
    } else if (n.type === "JSXExpressionContainer" && parent?.type !== "JSXAttribute") {
      const e = n.expression as { type?: string; value?: unknown } | undefined;
      if (e?.type === "Literal" && typeof e.value === "string" && /[A-Za-z]{2,}/.test(e.value) && !allowed(e.value)) {
        hits.push({ line: lineAt(src, n.start ?? 0), text: e.value });
      }
    }
    for (const [k, v] of Object.entries(n)) {
      if (k === "parent") continue;
      if (Array.isArray(v)) v.forEach((c) => visit(c, n));
      else if (v && typeof v === "object") visit(v, n);
    }
  };
  ast.body.forEach((b) => visit(b, null));
  return hits;
}

const SRC = fileURLToPath(new URL("..", import.meta.url));

function tsx(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) return tsx(path);
    return /\.tsx$/.test(name) && !/\.test\.tsx$/.test(name) ? [path] : [];
  });
}

describe("JSX text an operator reads", () => {
  const files = [...tsx(join(SRC, "pages")), ...tsx(join(SRC, "components"))];

  it("the parser reaches the sources and sees JSX at all", () => {
    // Positive control: a parse that found nothing would pass the rule below.
    expect(files.length).toBeGreaterThan(80);
    expect(scan(`const a = <p title="Plain words">Hello there</p>;`)).toHaveLength(2);
    expect(scan(`const a = <p title="dest.help">{t("x")} 12 ms</p>;`)).toHaveLength(0);
  });

  it("comes from the catalogue in pages/ and components/", () => {
    const hits = files.flatMap((f) =>
      scan(readFileSync(f, "utf8")).map((h) => `${relative(SRC, f)}:${h.line}  ${h.text.slice(0, 80)}`),
    );
    expect(hits, "move these into the catalogue (lib/i18n/*.json) and render them with t() / tn()").toEqual([]);
  });
});
