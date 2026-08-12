import { execFileSync } from "node:child_process";

import { expect, test } from "@playwright/test";

import { watchConsole } from "./console";
import { apiFetch } from "./destinations";

/* ===========================================================================
   #255: a pull source that ALREADY names an upload nothing ever inspected.

   Saving one is refused -- internal/api/source_pull_verdict_test.go drives that
   through both source routes and both controls. What that gate cannot see, by
   design, is state the operator inherited: a URL saved while the upload still
   had a passing record, or one written by a build that predates the gate. #255
   offers two remedies for it, refuse at engine reconcile or say so on the card,
   and the answer is that WHICH ONE depends on the state: an upload merely
   stored unchecked keeps streaming and is reported here, and one that was
   inspected and REFUSED stops the ingest at reconcile
   (Engine.pullUploadRefusal, which carries the argument for the split).

   This suite is the first of those two, and the verdict below is unverified for
   that reason -- a refused one would take the ingest off air and there would be
   no running source to carry the sentence.

   The state is built the way it really arises rather than by stubbing the
   response: a real upload with a real verdict beside it, saved through the real
   API while that verdict still says verified, and then DOWNGRADED underneath.
   Stubbing GET /sources would make this a test of the JSX and prove nothing
   about whether the server computes the field -- and the server computing it is
   the whole reason this is not just a badge (a monitoring script reading
   /sources sees it too; the case #201 named is automation that configures a
   pull source from a listing and never looks at a card).

   THE CONTROL IS THE SAME SOURCE, ONE MOMENT EARLIER. A card that always
   carried this warning, or one that carried it for every pull source, would
   pass a bare presence assertion. What is asserted here is the DIFFERENCE
   between two loads of the same page across a change to one file on disk.
   =========================================================================== */

const CONTAINER = process.env.E2E_CONTAINER ?? "poly-browser";
const UPLOAD = "e2e-inherited-abcd1234.ts";
const REASON = "the inspection was cut short before it finished";

/** Runs a shell command inside the container. This suite's os.WriteFile.
 *
 *  The verdict sidecar has no API that writes it directly: uploads.Store writes
 *  one from Pending.Commit when the bytes arrive, and the only way to get an
 *  UNVERIFIED one on an install whose ffprobe works is for the inspection to
 *  fail -- a dropped connection, a fork that could not start. Reproducing that
 *  through the upload route is not something a browser can arrange, and the
 *  API deliberately refuses to create this state on purpose. Same reasoning as
 *  removeUploadOutOfBand in playlist-editor.spec.ts. */
function inContainer(script: string) {
  execFileSync("docker", ["exec", CONTAINER, "sh", "-c", script], { stdio: "pipe" });
}

/** Writes the upload and a verdict beside it. `verified` picks which. */
function seedUpload(verified: boolean) {
  const verdict = verified
    ? `{"verified":true,"info":{"videoCodec":"h264","durationSeconds":1}}`
    : `{"verified":false,"reason":"${REASON}"}`;
  inContainer(
    `mkdir -p /data/uploads && printf 'x' > '/data/uploads/${UPLOAD}' && ` +
      `printf '%s' '${verdict}' > '/data/uploads/.probe-${UPLOAD}.json'`,
  );
}

function removeUpload() {
  inContainer(`rm -f -- '/data/uploads/${UPLOAD}' '/data/uploads/.probe-${UPLOAD}.json'`);
}

test.describe("a source pulling from an upload nothing inspected says so", () => {
  test("the warning appears when the verdict is downgraded under a saved source", async ({
    page,
  }) => {
    const console_ = watchConsole(page);
    await page.goto("/sources");
    await expect(page.locator("nav")).toBeVisible();

    // Purge first, for the reason destinations.ts records: a run that fails
    // part-way leaves a row behind, and the next run finds two cards with the
    // same name and dies on a strict-mode violation naming neither.
    const existing = await apiFetch<Array<{ id: number; name: string }>>(
      page,
      "GET",
      "/api/v1/sources",
    );
    for (const row of existing) {
      if (row.name === "e2e unchecked pull") {
        await apiFetch(page, "DELETE", `/api/v1/sources/${row.id}`);
      }
    }

    seedUpload(true);
    let created: { id: number } | null = null;
    try {
      // Saved while the verdict still passes. This save going through is itself
      // the control for the gate: a server that refused every file:// pull
      // source would fail here rather than below.
      created = await apiFetch<{ id: number }>(page, "POST", "/api/v1/sources", {
        name: "e2e unchecked pull",
        ingest: {
          mode: "pull",
          srt: { passphrase: "", latencyMs: 200 },
          rtmp: { app: "live", streamKey: "" },
          pull: {
            url: `file://uploads/${UPLOAD}`,
            reconnectDelayMaxSeconds: 30,
            rtspTransport: "tcp",
          },
        },
      });

      await page.reload();
      // The card has to be ON SCREEN before anything is concluded from the
      // ABSENCE of a warning below: "no warning" is also what a page that never
      // rendered the source looks like.
      await expect(
        page.getByText("e2e unchecked pull", { exact: true }),
        "the source that was just created is not on the page, so the absence " +
          "asserted next would prove nothing",
      ).toBeVisible();

      // BEFORE: nothing to say. Without this the assertion below passes on a
      // page that warns about every pull source, which would be a worse defect
      // than the silence it replaces -- an operator who sees the warning on
      // sources that are fine stops reading it.
      await expect(
        page.getByTestId("pull-unchecked"),
        "a source pulling from an upload this server INSPECTED AND ACCEPTED is " +
          "warned about anyway, so the warning says nothing and will be ignored",
      ).toBeHidden();

      // The verdict is downgraded underneath, which is exactly the shape the
      // save-time gate cannot see: no save happens, so no gate runs.
      seedUpload(false);
      await page.reload();

      const warning = page.getByTestId("pull-unchecked");
      await expect(
        warning,
        "a source pulling from an upload recorded as never inspected shows nothing " +
          "at all, so the only thing between an uninspected file and air is an " +
          "operator remembering a Library row they may never have seen",
      ).toBeVisible();

      // CONTENT, not presence. The file name is what tells the operator which
      // upload to send again, and the reason is what tells them it is the
      // server's inspection that failed rather than their file -- two facts a
      // bare orange badge does not carry, and the difference between noticing
      // and fixing.
      await expect(
        warning,
        "the warning does not name the upload, so an operator with several has " +
          "no way to tell which one to send again",
      ).toContainText(UPLOAD);
      await expect(
        warning,
        "the warning does not say why nothing read the file, so an operator " +
          "cannot tell a server-side inspection failure from a bad upload",
      ).toContainText(REASON);
    } finally {
      if (created) await apiFetch(page, "DELETE", `/api/v1/sources/${created.id}`);
      removeUpload();
    }

    expect(console_.errors, "the sources page logged errors").toEqual([]);
  });
});
