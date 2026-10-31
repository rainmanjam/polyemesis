import { Fragment, useMemo, useState } from "react";
import { Link } from "react-router";
import { ChevronRight, FileText, SlidersHorizontal } from "lucide-react";
import { Button } from "@/components/ui/button";
import { StatusPill } from "@/components/StatusPill";
import { duration } from "@/lib/format";
import { remedyFor } from "@/lib/failureRemedy";
import { useT } from "@/lib/i18n";
import { processStatus } from "@/lib/status";
import type { ProcessInfo, ProcessState } from "@/lib/types";
import { cn } from "@/lib/utils";

/* THE MONITORING PROCESS LIST AS A TABLE (app-council A72).
 *
 * It was one card per process, about 110px each, so eleven processes filled
 * two screens and a failed one sat wherever the supervisor happened to list
 * it. Now one row per process carries what is read at a glance -- state,
 * restarts, uptime, errors -- and the rows that need a look come first.
 *
 * Everything else the card held is one click away in the row's detail: the
 * classified cause and its remedy (A9), the raw last error when the cause is
 * unclassified, the retry countdown, the PID, the log and the full command.
 * MonitoringPage.processTable.test.tsx lists every piece and finds each one. */

/** Failed and reconnecting are the states an operator acts on. Starting is a
 *  transition everyone passes through, so it stays with the rest. */
const PROBLEM: ReadonlySet<ProcessState> = new Set(["failed", "reconnecting"]);

/** Problems first, everything else in the order the server listed it. A
 *  stable partition on a copy: the caller's array feeds the verdict and the
 *  log filter, and must not be reordered under them. */
function problemsFirst(processes: readonly ProcessInfo[]): ProcessInfo[] {
  const problems = processes.filter((p) => PROBLEM.has(p.status.state));
  const rest = processes.filter((p) => !PROBLEM.has(p.status.state));
  return [...problems, ...rest];
}

/** A DOM id from a process name. Names hold ":" ("dest:7"), which is a legal
 *  id character but an awkward one in selectors, so it is replaced. */
const detailId = (name: string) => `proc-detail-${name.replace(/[^A-Za-z0-9_-]/g, "-")}`;

export function ProcessTable({
  processes,
  errorsByProcess,
  onShowLog,
}: Readonly<{
  processes: readonly ProcessInfo[];
  errorsByProcess: ReadonlyMap<string, number>;
  onShowLog: (process: string) => void;
}>) {
  const t = useT();
  const rows = useMemo(() => problemsFirst(processes), [processes]);
  // Keyed by name, not by index: the five-second poll can re-sort the rows,
  // and an open row must stay with its process.
  const [open, setOpen] = useState<ReadonlySet<string>>(() => new Set());
  const [commandOpen, setCommandOpen] = useState<ReadonlySet<string>>(() => new Set());
  const flip = (set: ReadonlySet<string>, name: string) => {
    const next = new Set(set);
    if (next.has(name)) next.delete(name);
    else next.add(name);
    return next;
  };

  return (
    <div className="overflow-x-auto">
      <table className="w-full border-collapse text-sm">
        <caption className="sr-only">{t("procTable.caption")}</caption>
        <thead>
          <tr className="border-b border-border text-left text-muted-foreground">
            <th scope="col" className="py-1.5 pr-2 font-medium">{t("procTable.colProcess")}</th>
            <th scope="col" className="py-1.5 pr-2 font-medium">{t("procTable.colState")}</th>
            <th scope="col" className="py-1.5 pr-2 font-medium">{t("mon.restarts")}</th>
            <th scope="col" className="py-1.5 pr-2 font-medium">{t("mon.uptime")}</th>
            <th scope="col" className="py-1.5 font-medium">{t("procTable.colErrors")}</th>
          </tr>
        </thead>
        <tbody>
          {rows.map(({ status: p, command }) => {
            const expanded = open.has(p.name);
            const problem = PROBLEM.has(p.state);
            const errors = errorsByProcess.get(p.name) ?? 0;
            const id = detailId(p.name);
            const remedy = p.reason ? remedyFor(p.reason.cause) : null;
            const showingCommand = commandOpen.has(p.name);
            return (
              <Fragment key={p.name}>
                <tr className={cn("border-b border-border", problem && "bg-down-dim/40")}>
                  <th scope="row" className="py-1 pr-2 text-left font-normal">
                    <button
                      type="button"
                      aria-expanded={expanded}
                      aria-controls={expanded ? id : undefined}
                      aria-label={t("procTable.toggle", { process: p.name })}
                      onClick={() => setOpen((s) => flip(s, p.name))}
                      className="flex min-w-0 items-center gap-1 rounded font-mono hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring"
                    >
                      <ChevronRight
                        aria-hidden="true"
                        className={cn("h-3.5 w-3.5 shrink-0 transition-transform", expanded && "rotate-90")}
                      />
                      <span className="truncate">{p.name}</span>
                    </button>
                  </th>
                  <td className="py-1 pr-2">
                    <StatusPill reading={processStatus(p.state)} />
                  </td>
                  <td className={cn("tnum py-1 pr-2 font-mono", p.restarts > 0 && "text-warn")}>{p.restarts}</td>
                  <td className="tnum py-1 pr-2 font-mono">
                    {p.state === "running" ? duration(p.uptimeSec) : "—"}
                  </td>
                  <td className={cn("tnum py-1 font-mono", errors > 0 && "text-warn")}>{errors}</td>
                </tr>
                {expanded && (
                  <tr id={id} className="border-b border-border bg-surface/50">
                    <td colSpan={5} className="px-5 py-2">
                      <div className="flex flex-col items-start gap-1.5">
                        {/* #20 + A9: the classified cause and what to do about
                            it, from the same map the destination card reads.
                            The raw lastError stays for an unclassified failure. */}
                        {p.reason && remedy ? (
                          <div className="flex flex-col gap-0.5 text-down">
                            <span className="font-medium">{t(`reason.cause.${p.reason.cause}`)}</span>
                            <span>{t(remedy.text)}</span>
                          </div>
                        ) : (
                          p.lastError && <div className="break-words text-down">{p.lastError}</div>
                        )}
                        {p.nextRetryIn ? (
                          <div className="text-warn">
                            {t("i18nSweep.retryingIn", { seconds: p.nextRetryIn.toFixed(0) })}
                          </div>
                        ) : null}
                        <div className="text-muted-foreground">
                          {t("mon.pid")} <span className="tnum font-mono">{p.pid || "—"}</span>
                        </div>
                        <div className="flex flex-wrap items-center gap-1.5">
                          {remedy?.action === "renditions" && (
                            <Button size="sm" variant="outline" asChild>
                              <Link to="/renditions">
                                <SlidersHorizontal /> {t("reason.action.renditions")}
                              </Link>
                            </Button>
                          )}
                          {/* One Show log for every row. On Monitoring the
                              remedy's "edit" has no dialog to open, so it is
                              this same button (FailureRemedy degrades it the
                              same way). */}
                          <Button size="sm" variant="outline" onClick={() => onShowLog(p.name)}>
                            <FileText /> {t("reason.action.log")}
                          </Button>
                          <Button
                            size="sm"
                            variant="ghost"
                            aria-expanded={showingCommand}
                            onClick={() => setCommandOpen((s) => flip(s, p.name))}
                          >
                            {showingCommand ? t("procTable.hideCommand") : t("procTable.showCommand")}
                          </Button>
                        </div>
                        {showingCommand && (
                          <pre className="max-h-32 w-full overflow-auto whitespace-pre-wrap break-all rounded bg-background p-1.5 font-mono text-sm text-muted-foreground">
                            {command}
                          </pre>
                        )}
                      </div>
                    </td>
                  </tr>
                )}
              </Fragment>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
