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

/** The same identifiers, appearing as the whole body of a `<code>` element.
 *
 *  A DISPLAYED credential is worse than an editable one and the guard above
 *  could not see it. SECRET_BINDING matches `value={...}`; the Sources page
 *  printed the publish token as `<code>{source.token}</code>` -- no `value`,
 *  no `<Input>`, invisible to every check here -- twice, on the page an
 *  operator opens while someone is helping them go live. It shipped, reviewed
 *  clean, and was found by looking at the running console.
 *
 *  Same rung and same known hole as SECRET_BINDING: it matches the expression,
 *  so a rename defeats it. It catches the mistake that has now actually
 *  happened. */
const SECRET_PRINTED =
  /<code[^>]*>\s*\{\s*[^}]*\b(streamKey|passphrase|clientSecret|plaintext|newSecret|[a-zA-Z]*[Ss]ecret|[a-zA-Z]*[Pp]assword|apiKey|[a-zA-Z]*[Tt]oken)\b[^}]*\}\s*<\/code>/;

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

/** Comments stripped before matching.
 *
 *  A guard that reads prose reports the docstring explaining the bug as the
 *  bug. SecretCode's own comment quotes `<code>{source.token}</code>` verbatim,
 *  which is exactly the string this looks for — so the first run of this check
 *  failed on the file that fixes it. */
function code(src: string): string {
  return src.replace(/\/\*[\s\S]*?\*\//g, "").replace(/^\s*\/\/.*$/gm, "");
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

  it("no credential is printed into a <code> block", () => {
    // The read-only half of the same rule. SecretCode is the fix: masked to a
    // fixed width, with a deliberate reveal, and Copy still works while masked.
    const offenders: string[] = [];

    for (const file of files) {
      const src = code(readFileSync(file, "utf8"));
      const m = SECRET_PRINTED.exec(src);
      if (!m) continue;
      const line = src.slice(0, m.index).split("\n").length;
      offenders.push(`${file}:${line}  ${m[0].replace(/\s+/g, " ").slice(0, 80)}`);
    }

    expect(
      offenders,
      "These render a credential as readable text on screen, which is the " +
        "projector case SecretInput was written for -- but read-only, so it " +
        "cannot be typed over or masked by the browser. Use <SecretCode>:\n  " +
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
