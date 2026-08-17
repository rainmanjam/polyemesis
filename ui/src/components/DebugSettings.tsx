import { useCallback, useEffect, useState } from "react";

import { ConfirmDestructive } from "@/components/ConfirmDestructive";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Switch } from "@/components/ui/switch";
import { api } from "@/lib/api";
import { bytes as formatBytes } from "@/lib/format";
import { useT } from "@/lib/i18n";
import type { DebugState } from "@/lib/types";

/* ===========================================================================
   DEBUG MODE, IN SETTINGS, ON ITS OWN TAB.

   Placed here rather than on Monitoring, which is the other candidate: this is
   a SETTING an operator changes and leaves changed, not a reading they watch.
   It also sits beside Security for a reason that is not filing — the export is
   the only control in this product that hands a copy of the server's own logs
   to somebody else, and the tab an operator reaches for when thinking about
   disclosure is the one next to it.

   TWO CONTROLS AT DELIBERATELY DIFFERENT WEIGHTS, mirroring the API exactly:

     RECORDING is a switch. It changes what this box writes down, nothing
     leaves the machine, and it is reversible in one click. A confirmation here
     would be the reflex-training ConfirmDestructive warns about.

     EXPORT confirms. It produces a file meant to be sent to somebody who does
     not have the server, which is the one irreversible thing available here --
     not because the file cannot be deleted, but because a sent one cannot be
     unsent.

   AND IT DOES NOT USE requireTyping, deliberately, against the first instinct.
   That flag is reserved by its own doctrine for "things that do not come back:
   a deleted file, a revoked credential, a cascade", and the warning attached is
   that using it on recoverable actions "is how a control becomes a reflex, and
   a reflex is not a control". Producing a bundle IS recoverable — the operator
   can delete it. What is irreversible is sending it, and polyemesis does not
   send it. So this gets a confirmation that states plainly what is inside,
   which is the decision actually being made.

   THE CONSEQUENCES PANEL CARRIES THE COUNTS, for ConfirmDestructive's own
   stated reason: "confirming a number is a decision, confirming a vibe is a
   click." The number of log lines about to leave is exactly the fact somebody
   needs in order to decide.
   =========================================================================== */

export function DebugSettings() {
  const t = useT();
  const [state, setState] = useState<DebugState | null>(null);
  const [busy, setBusy] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const [error, setError] = useState("");

  const refresh = useCallback(async () => {
    try {
      setState(await api.debugState());
      setError("");
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  // POLLED WHILE RECORDING, and only then. The counts move as the buffer fills,
  // and an operator watching a reproduction wants to see that it is capturing
  // rather than to wonder. Off, there is nothing to poll for: the numbers only
  // change when recording.
  useEffect(() => {
    if (!state?.recording) return;
    const id = window.setInterval(() => void refresh(), 2000);
    return () => window.clearInterval(id);
  }, [state?.recording, refresh]);

  const toggle = async (on: boolean) => {
    setBusy(true);
    try {
      setState(await api.setDebug({ recording: on }));
      setError("");
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  const clear = async () => {
    setBusy(true);
    try {
      setState(await api.setDebug({ reset: true }));
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  const runExport = async () => {
    setBusy(true);
    try {
      const { text, filename } = await api.exportDebug();
      // Handed to the browser rather than opened: the operator is about to
      // attach this to something, and a file on disk is what they need.
      const url = URL.createObjectURL(new Blob([text], { type: "application/json" }));
      const a = document.createElement("a");
      a.href = url;
      a.download = filename;
      a.click();
      URL.revokeObjectURL(url);
      setError("");
      void refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  const held = state?.held ?? 0;
  const seen = state?.seen ?? 0;
  const truncated = seen > held;
  const size = state?.bytes ?? 0;
  // Lines cut at the per-record cap. A DIFFERENT CLAIM from `truncated` above,
  // which counts whole lines the ring dropped: one explains a missing line, the
  // other a line that stops mid-sentence, and an engineer needs both.
  const cutLines = state?.recordsTruncated ?? 0;

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("debug.title")}</CardTitle>
        <CardDescription>{t("debug.blurb")}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="flex items-center justify-between gap-4">
          <div className="space-y-1">
            <div className="text-sm font-medium">{t("debug.recordLabel")}</div>
            {/* The level is stated because "debug mode is on" and "the level is
                actually debug" are different claims, and the commonest reason a
                capture comes back useless is that it was taken at info. */}
            <div className="text-xs text-muted-foreground">
              {t("debug.recordHint")}
              {state?.level ? ` (${state.level})` : ""}
            </div>
          </div>
          <Switch
            checked={state?.recording ?? false}
            disabled={busy || !state}
            onCheckedChange={(v) => void toggle(v)}
            aria-label={t("debug.recordLabel")}
          />
        </div>

        <div className="text-sm tnum">
          {t("debug.held")}: {held.toLocaleString()}
          {/* The SIZE beside the count, because the count is what the ring
              bounds and the size is what the operator is about to send. */}
          {size > 0 ? (
            <span className="ml-2 text-xs text-muted-foreground">{formatBytes(size)}</span>
          ) : null}
          {cutLines > 0 ? (
            // Long lines were shortened to fit the per-record cap. Said here as
            // well as in the bundle, so an engineer told "the log just stops" has
            // the explanation before they ask.
            <span className="ml-2 text-xs text-muted-foreground">
              {t("debug.linesCut", { count: cutLines.toLocaleString() })}
            </span>
          ) : null}
          {truncated ? (
            // SAID OUT LOUD. A buffer that dropped its oldest records and shows
            // the rest as though they were all of them is how an engineer reads
            // a capture and concludes the fault left no trace.
            <span className="ml-2 text-xs text-muted-foreground">
              {t("debug.truncated", { seen: seen.toLocaleString() })}
            </span>
          ) : null}
        </div>

        {error ? (
          <div role="alert" className="text-sm text-destructive">
            {error}
          </div>
        ) : null}

        <div className="flex flex-wrap gap-2">
          <Button
            variant="outline"
            disabled={busy || held === 0}
            onClick={() => setConfirming(true)}
          >
            {t("debug.export")}
          </Button>
          <Button variant="ghost" disabled={busy || held === 0} onClick={() => void clear()}>
            {t("debug.clear")}
          </Button>
        </div>
      </CardContent>

      <ConfirmDestructive
        open={confirming}
        onOpenChange={setConfirming}
        subject={t("debug.title")}
        title={t("debug.confirmTitle")}
        description={
          <>
            {t("debug.confirmBody")}{" "}
            {/* THE SIZE IS PART OF THE DECISION. "Confirming a number is a
                decision, confirming a vibe is a click" is why the count is
                here; how large the file is decides whether it can be attached
                to the thread they were going to attach it to. */}
            {size > 0 ? t("debug.confirmSize", { size: formatBytes(size) }) : null}
          </>
        }
        consequences={[{ label: t("debug.consequenceRecords"), count: held }]}
        consequencesLabel={t("debug.consequencesLabel")}
        confirmLabel={t("debug.confirmAction")}
        onConfirm={runExport}
      />
    </Card>
  );
}
