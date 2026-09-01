import { readFileSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

/* A CREDENTIAL ON A PLAIN <Input> IS A CREDENTIAL ON THE PROJECTOR.
 *
 * SecretInput was written for exactly this, and its own docstring names the
 * threat: these fields are edited during the activity that puts a screen in
 * front of an audience -- setting up a broadcast, often while screen-sharing
 * with someone helping. It was then used on ONE field out of seven.
 *
 * The others were not merely un-revealable. SettingsPage's RTMP ingest key had
 * no `type` at all, so the credential OBS authenticates with rendered as plain
 * readable text in Settings, and nothing on the page suggested it was a secret.
 * Five more were `type="password"` -- masked, but with no way to reveal, so an
 * operator checking a pasted key had to save and re-open, or select-and-copy it
 * somewhere less safe to read it.
 *
 * Both failures have the same cause and it is not carelessness: the correct
 * component existed and NOTHING REQUIRED IT. `<Input type="password">` looks
 * right, reviews as right, and is one word shorter.
 *
 * So this asks the question no reviewer reliably remembers to: does any input
 * in this tree hold a value whose NAME says it is a secret, without being a
 * SecretInput? That is a Warning-rung device -- CI announces it rather than the
 * type system forbidding it -- because the value is bound by an arbitrary
 * expression and React gives us no type that says "this string is sensitive".
 * Control would mean a branded Secret<string> threaded through the API client
 * and every draft object, which is a real design and a much larger change than
 * the bug warrants today.
 *
 * IT MATCHES ON THE VALUE EXPRESSION, NOT THE COMPONENT. A field renamed from
 * `streamKey` to something unrecognisable slips past, and that is the known
 * hole: this catches the mistake that has actually happened six times, not
 * every mistake imaginable. */

/** Identifiers whose presence in a `value={...}` binding means the input holds a
 *  credential. Deliberately about the DATA, not the widget. */
const SECRET_BINDING =
  /value=\{[^}]*\b(streamKey|passphrase|clientSecret|[a-zA-Z]*[Ss]ecret|[a-zA-Z]*[Pp]assword|apiKey|[a-zA-Z]*Token)\b[^}]*\}/;

/** Fields that are legitimately a plain input, each with the reason.
 *
 *  Paths are relative to `ui/`, which is vitest's working directory.
 *
 *  A login password is not the same object as a stream key. It is typed once,
 *  by the person who owns it, into a form browsers and password managers
 *  recognise BY SHAPE -- `type="password"` plus an autoComplete hint is what
 *  makes autofill work at all. Wrapping it changes a flow that is correct. */
const ALLOWED: ReadonlyArray<{ file: string; id: string; why: string }> = [
  {
    file: "src/pages/AuthScreen.tsx",
    id: "password",
    why: "login field: password managers key off type=password + autoComplete; a reveal is not the same trade-off as on a shared broadcast console",
  },
];

/** Every .tsx under src/, minus tests. Walked rather than globbed: `fs.globSync`
 *  is not in this TypeScript lib's node types, and tour-drift.test.ts already
 *  walks the tree this way. */
function tsxFiles(dir: string): string[] {
  return readdirSync(dir).flatMap((entry: string) => {
    const full = join(dir, entry);
    if (statSync(full).isDirectory()) return tsxFiles(full);
    return full.endsWith(".tsx") && !full.includes(".test.") ? [full] : [];
  });
}

const files = tsxFiles("src");

describe("secret fields", () => {
  it("finds source to scan", () => {
    // A glob that silently matches nothing would make every assertion below
    // vacuously true -- the failure mode this whole file exists to prevent.
    expect(files.length).toBeGreaterThan(10);
  });

  it("no credential is bound to a plain <Input>", () => {
    const offenders: string[] = [];

    for (const file of files) {
      const src = readFileSync(file, "utf8");
      // Split on element starts so each chunk is one element's props.
      for (const chunk of src.split(/<(?=[A-Z])/)) {
        if (!chunk.startsWith("Input")) continue;
        const props = chunk.slice(0, chunk.indexOf(">") + 1);
        if (!SECRET_BINDING.test(props)) continue;

        const id = /id=\{?["`]([^"`}]+)/.exec(props)?.[1] ?? "";
        const excused = ALLOWED.some((a) => file.endsWith(a.file) && a.id === id);
        if (!excused) {
          const line = src.slice(0, src.indexOf(props)).split("\n").length;
          offenders.push(`${file}:${line}  id=${id || "(none)"}`);
        }
      }
    }

    expect(
      offenders,
      "These inputs hold a credential but are not SecretInput, so the value is " +
        "readable on screen and cannot be deliberately revealed or re-hidden. " +
        "Use <SecretInput>, or add an entry to ALLOWED with the reason:\n  " +
        offenders.join("\n  "),
    ).toEqual([]);
  });

  it("every allowed exception still exists", () => {
    // An excuse for a field that has been deleted or renamed is an excuse that
    // will silently cover the NEXT field to take that id.
    for (const a of ALLOWED) {
      const src = readFileSync(a.file, "utf8");
      expect(src, `${a.file} no longer contains id="${a.id}"`).toContain(`id="${a.id}"`);
    }
  });
});
