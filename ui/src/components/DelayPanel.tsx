import { useState } from "react";
import { FastForward, Scissors, Timer } from "lucide-react";
import { Button } from "@/components/ui/button";
import { ConfirmDestructive } from "@/components/ConfirmDestructive";
import { Input } from "@/components/ui/input";
import type { DelayDumpResult, DestStatus } from "@/lib/types";
import { useT } from "@/lib/i18n";
import { cn } from "@/lib/utils";

/** A destination's broadcast delay on its card (#11).
 *
 *  Three states, each answering the question the operator has at that moment:
 *
 *  - buffering: "when does this go out?" -- the fill, against the delay.
 *  - on air: "is this delayed?" -- a marker, so nobody wonders why the
 *    platform is behind the preview.
 *  - draining: "is this still on the air after I stopped it?" -- YES, for
 *    this long, with the control that ends it. A delay exists to keep things
 *    off the air, so the cut is on the card, not behind a menu.
 *
 *  On air it also carries Dump: drop the newest part of the buffer and stay
 *  on the air. Viewers skip forward, and the delay STAYS SHORTER until the
 *  destination next starts -- it cannot grow back while on air -- so the
 *  badge says what the audience's delay is now, not only what was set. */
export function DelayPanel({
  dest,
  onCutNow,
  onDump,
  busy,
}: Readonly<{
  dest: DestStatus;
  /** Discards the buffered tail. Absent for a card that cannot act. */
  onCutNow?: () => void;
  /** Dumps the newest part of the buffer; `seconds` 0 is "everything since
   *  the last dump point", and no `seconds` is the destination's default. Resolves with the server's answer and rejects with
   *  its reason. Absent for a card that cannot act. */
  onDump?: (seconds?: number) => Promise<DelayDumpResult>;
  busy?: boolean;
}>) {
  const t = useT();
  const [asking, setAsking] = useState(false);
  const [mode, setMode] = useState<"since" | "seconds">("since");
  const [amount, setAmount] = useState("10");
  const [result, setResult] = useState<
    { ok: true; answer: DelayDumpResult } | { ok: false; message: string } | null
  >(null);
  const d = dest.delay;
  // A dump's figures describe THIS run. Once the destination stops or starts
  // again the delay is back to what was set, and the old "delay now 18.6 s"
  // beside a full badge would contradict it, so a phase change clears it.
  // Adjusted during render rather than in an effect, so it never paints stale.
  const [shownPhase, setShownPhase] = useState(d?.phase);
  if (d?.phase !== shownPhase) {
    setShownPhase(d?.phase);
    setResult(null);
  }
  const seconds = d?.seconds ?? dest.delaySeconds ?? 0;
  if (!seconds) return null;

  if (d?.phase === "draining") {
    return (
      <output
        data-testid="delay-draining"
        className="flex items-center gap-2 rounded border border-down/30 bg-down-dim px-2 py-1 text-sm text-down"
      >
        <Timer className="h-3 w-3 shrink-0" />
        <span className="flex-1">
          {t("delay.draining", { seconds: Math.ceil(d.drainRemainingSeconds ?? 0) })}
        </span>
        {onCutNow && (
          <Button
            size="sm"
            variant="destructive"
            onClick={onCutNow}
            disabled={busy}
            title={t("delay.cutNowTitle", { name: dest.name })}
          >
            <Scissors /> {t("delay.cutNow")}
          </Button>
        )}
      </output>
    );
  }

  if (d?.phase === "buffering") {
    const pct = Math.round(Math.min(1, Math.max(0, d.fill)) * 100);
    return (
      <div data-testid="delay-buffering" className="flex flex-col gap-1 text-sm text-muted-foreground">
        <span className="flex items-center gap-1.5">
          <Timer className="h-3 w-3 shrink-0" />
          {t("delay.buffering", { buffered: Math.floor(d.bufferedSeconds), seconds })}
        </span>
        <div
          className="h-1 w-full overflow-hidden rounded bg-muted"
          role="progressbar"
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={pct}
          aria-label={t("delay.fillLabel")}
        >
          <div className="h-full bg-primary" style={{ width: `${pct}%` }} />
        </div>
      </div>
    );
  }

  const onAir = d?.phase === "on-air";
  const effective = d?.effectiveSeconds ?? seconds;
  const dumped = (d?.dumpedSeconds ?? 0) > 0;
  const n = Number(amount);
  const amountValid = Number.isFinite(n) && n >= 1 && n <= 900;
  const estimate = Math.round(Math.max(effective - (amountValid ? n : 0), 2));

  const badge = (
    <span
      data-testid="delay-badge"
      className={cn(
        "inline-flex w-fit items-center gap-1 rounded border px-1.5 py-0.5 text-[11px]",
        onAir ? "border-primary/40 text-primary-bright" : "border-border text-muted-foreground",
      )}
    >
      <Timer className="h-3 w-3" />
      {dumped
        ? t("delay.badgeDumped", { effective: Math.round(effective), seconds })
        : t("delay.badge", { seconds })}
    </span>
  );

  if (!onAir) {
    return badge;
  }

  // The destination's own Dump default is preselected, so the usual dump is
  // one confirm away; without one, "everything since the last dump point".
  const preset = Math.round(dest.delayDumpSeconds ?? 0);
  const ask = () => {
    setMode(preset > 0 ? "seconds" : "since");
    setAmount(preset > 0 ? String(preset) : "10");
    setResult(null);
    setAsking(true);
  };

  const dump = async () => {
    if (!onDump) return;
    try {
      // "Everything since the last dump point" is an explicit 0: with no
      // seconds the server would apply the destination's default instead.
      const answer = await onDump(mode === "seconds" ? n : 0);
      setResult({ ok: true, answer });
    } catch (err) {
      setResult({
        ok: false,
        message: err instanceof Error ? err.message : String(err),
      });
    }
  };

  const fig = (x: number) => Number(x.toFixed(1));

  return (
    <div data-testid="delay-on-air" className="flex flex-col gap-1">
      <div className="flex flex-wrap items-center gap-2">
        {badge}
        {onDump && (
          <Button
            size="sm"
            variant="destructive"
            data-testid="delay-dump"
            onClick={ask}
            disabled={!d.dumpable || d.dumping || busy}
            title={t("delay.dumpTitle", { name: dest.name })}
          >
            <FastForward /> {t("delay.dump")}
          </Button>
        )}
        {d.dumping && <span className="text-[11px] text-muted-foreground">{t("delay.dumping")}</span>}
      </div>
      {dumped && (
        <p data-testid="delay-restores" className="text-[11px] text-muted-foreground">
          {t("delay.restoresOnStart", { seconds })}
        </p>
      )}
      {result && (
        <p
          data-testid="delay-dump-result"
          role="status"
          className={cn("text-[11px]", result.ok ? "text-muted-foreground" : "text-down")}
        >
          {result.ok
            ? [
                t("delay.dumped", {
                  dumped: fig(result.answer.dumpedSeconds),
                  after: fig(result.answer.delayAfterSeconds),
                }),
                result.answer.clamped ? t("delay.dumpClamped") : "",
                result.answer.complete ? "" : t("delay.dumpPending"),
              ]
                .filter(Boolean)
                .join(" ")
            : t("delay.dumpFailed", { message: result.message })}
        </p>
      )}
      {onDump && (
        <ConfirmDestructive
          open={asking}
          onOpenChange={setAsking}
          subject={dest.name}
          title={t("delay.dumpConfirmTitle")}
          consequence={t("delay.dumpConsequence", { name: dest.name })}
          confirmLabel={t("delay.dump")}
          confirmDisabled={mode === "seconds" && !amountValid}
          onConfirm={dump}
          description={
            <span className="flex flex-col gap-2">
              <span>{t("delay.dumpDescription")}</span>
              <label className="flex items-center gap-2">
                <input
                  type="radio"
                  name={`delay-dump-mode-${dest.id}`}
                  data-testid="delay-dump-mode-since"
                  checked={mode === "since"}
                  onChange={() => setMode("since")}
                />
                {t("delay.dumpSinceLast")}
              </label>
              <span className="flex items-center gap-2">
                <input
                  type="radio"
                  id={`delay-dump-mode-seconds-${dest.id}`}
                  name={`delay-dump-mode-${dest.id}`}
                  data-testid="delay-dump-mode-seconds"
                  checked={mode === "seconds"}
                  onChange={() => setMode("seconds")}
                />
                <label htmlFor={`delay-dump-mode-seconds-${dest.id}`}>{t("delay.dumpNewest")}</label>
                <Input
                  type="number"
                  min={1}
                  max={900}
                  step={1}
                  className="h-7 w-20"
                  data-testid="delay-dump-seconds"
                  aria-label={t("delay.dumpSecondsLabel")}
                  value={amount}
                  onFocus={() => setMode("seconds")}
                  onChange={(e) => {
                    setMode("seconds");
                    setAmount(e.target.value);
                  }}
                />
              </span>
              <span data-testid="delay-dump-before" className="font-medium text-foreground">
                {t("delay.dumpBefore", { seconds: Math.round(effective) })}
              </span>
              <span data-testid="delay-dump-after" className="font-medium text-foreground">
                {mode === "seconds"
                  ? t("delay.dumpAfterEstimate", { seconds: estimate })
                  : t("delay.dumpAfterAll")}
              </span>
              <span data-testid="delay-dump-overshoot" className="text-muted-foreground">
                {t("delay.dumpOvershoot")}
              </span>
            </span>
          }
        />
      )}
    </div>
  );
}
