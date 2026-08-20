/// <reference types="node" />
import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";

import { cancelledNames, classifyUpload, failureLines } from "./uploadOutcome";

/* WHAT A BATCH OF UPLOADS IS ALLOWED TO REPORT.
 *
 * One `error` slot and no notion of an abort produced two separate lies at
 * once: a cancel painted red, and every failure but the last discarded.
 */

describe("classifyUpload", () => {
  it("calls a deliberate cancel a cancel, not a failure", () => {
    // The abort rejects with ApiError(0, "upload cancelled") and the loop
    // caught it like any other rejection, so pressing Cancel put a red
    // border-destructive banner on screen. Read from the SIGNAL, not from the
    // message: the message is a string the API client happens to write today.
    expect(classifyUpload("clip.ts", true, "upload cancelled")).toEqual({
      name: "clip.ts",
      kind: "cancelled",
    });
  });

  it("calls a real rejection a failure", () => {
    expect(classifyUpload("clip.ts", false, "413 too large")).toEqual({
      name: "clip.ts",
      kind: "failed",
      detail: "413 too large",
    });
  });
});

describe("failureLines", () => {
  it("keeps every failure in a batch, not just the last one", () => {
    // THE DEFECT: `setError(...)` once per file in a sequential loop. Drop
    // five files, have three refused, and the operator sees one filename and
    // believes the other two are on the server.
    const lines = failureLines([
      { name: "a.ts", kind: "failed", detail: "413 too large" },
      { name: "b.ts", kind: "ok" },
      { name: "c.ts", kind: "failed", detail: "415 unsupported" },
      { name: "d.ts", kind: "failed", detail: "disk full" },
    ]);
    expect(lines).toEqual([
      "a.ts: 413 too large",
      "c.ts: 415 unsupported",
      "d.ts: disk full",
    ]);
  });

  it("does not put a cancelled upload in the destructive banner", () => {
    expect(
      failureLines([
        { name: "a.ts", kind: "cancelled" },
        { name: "b.ts", kind: "ok" },
      ]),
    ).toEqual([]);
  });
});

describe("cancelledNames", () => {
  it("names what the operator cancelled, so silence is not the answer either", () => {
    expect(
      cancelledNames([
        { name: "a.ts", kind: "cancelled" },
        { name: "b.ts", kind: "failed", detail: "boom" },
        { name: "c.ts", kind: "cancelled" },
      ]),
    ).toEqual(["a.ts", "c.ts"]);
  });
});

/* AND THAT THE CARD ACTUALLY USES THEM. */
const ROOT = new URL("../../../", import.meta.url).pathname;
const src = () => readFileSync(join(ROOT, "ui/src/components/MediaUploads.tsx"), "utf8");

describe("MediaUploads", () => {
  it("holds every failure rather than one slot", () => {
    expect(src()).not.toContain('const [error, setError] = useState("");');
    expect(src()).toContain("const [errors, setErrors] = useState<string[]>([]);");
    expect(src()).toContain("setErrors(failureLines(results));");
  });

  it("reads the abort off the signal and reports it in the muted notice", () => {
    expect(src()).toContain("controller.signal.aborted");
    expect(src()).toContain('setNotice(t("media.uploadCancelled"');
  });

  it("does not remount the in-flight row on every progress event", () => {
    // `key={f.name + f.fraction}` rebuilt the row several times a second, so
    // the Cancel button swallowed clicks and could not hold focus -- the one
    // control that stops a large upload was the one that did not work.
    expect(src()).not.toContain("key={f.name + f.fraction}");
    expect(src()).toContain('<div key={f.name} className="grid gap-1">');
  });
});
