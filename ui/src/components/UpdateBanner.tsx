import { useEffect, useState } from "react";
import { Check, Copy, ExternalLink, X } from "lucide-react";

import { api } from "../lib/api";
import { useT } from "../lib/i18n";
import type { UpgradePlan, UpgradeResult, VersionInfo } from "../lib/types";

/** Tells an operator a release exists, and -- where the install allows it --
 *  prepares that release without restarting anything.
 *
 *  The check itself has been in the server since before this component: two
 *  endpoints, a 6h TTL cache, honest handling of dev builds. Nothing rendered
 *  it, so an install could sit several releases behind with the answer sitting
 *  one HTTP call away and no way to see it.
 *
 *  NOTHING HERE RESTARTS ANYTHING, and the wording never says "updated".
 *  Preparing an update swaps the binary on disk; the running process keeps
 *  running the old code until somebody restarts the service, which is the
 *  moment every destination drops. Telling an operator they are "up to date"
 *  when they are one restart away from it is how a fix gets believed in and not
 *  applied.
 *
 *  THE PLAN IS FETCHED ON THE OPERATOR'S CLICK, never on mount. Building it
 *  probes whether the install directory can be written to by creating a file in
 *  it, and this component mounts on every page.
 *
 *  A REFUSAL IS THE NORMAL ANSWER ON A STOCK INSTALL. The systemd unit runs
 *  with ProtectSystem=strict, so /usr/local/bin is read-only to the service and
 *  the server answers with the command to run instead. That is rendered as
 *  instructions, not as an error, because it is not one. */
/** onInfo reports the version answer upward as soon as it arrives.
 *
 *  The chrome shows the running version beside the username, and it must not
 *  fetch /version a second time to learn it: that endpoint surveys what a
 *  restart would interrupt FRESH ON EVERY CALL, so a second caller means a
 *  second on-air survey on every page load. This component already holds the
 *  answer -- including the refreshed one after a check -- so it hands it over
 *  rather than making the chrome ask again. #660. */
export function UpdateBanner({ onInfo }: { onInfo?: (v: VersionInfo) => void }) {
  const t = useT();
  const [info, setInfo] = useState<VersionInfo | null>(null);
  const [dismissed, setDismissed] = useState(false);

  // What the operator's action has produced so far. "idle" is the banner as it
  // has always been: a sentence and a link.
  const [stage, setStage] = useState<
    "idle" | "planning" | "manual" | "confirm" | "working" | "done"
  >("idle");
  const [plan, setPlan] = useState<UpgradePlan | null>(null);
  const [result, setResult] = useState<UpgradeResult | null>(null);
  const [error, setError] = useState("");
  // Whether the on-air refusal was overridden, so an undo can get past the same
  // gate the stage did. Something that was live a moment ago still is.
  const [forced, setForced] = useState(false);

  useEffect(() => {
    let alive = true;
    // The CACHED answer on mount, never a check. Making every page load reach
    // GitHub would turn a pull-only design into a phone-home one, which is the
    // property this feature was built around.
    api
      .version()
      .then((v) => {
        if (!alive) return;
        setInfo(v);
        onInfo?.(v);
        // One check per session, and only when the server has never run one --
        // otherwise an install nobody clicks would never learn anything.
        if (!v.checkedAt) {
          api
            .checkUpdate()
            .then((fresh) => {
              if (!alive) return;
              setInfo(fresh);
              onInfo?.(fresh);
            })
            .catch(() => {});
        }
      })
      .catch(() => {});
    return () => {
      alive = false;
    };
  }, [onInfo]);

  if (!info || dismissed) return null;
  // A failed check says nothing and must look like nothing: an operator whose
  // box has no outbound network should not see a permanent warning about it.
  if (info.checkFailed) return null;

  // A BUILD FROM SOURCE SAYS SO, rather than saying nothing.
  //
  // The server reports `comparable: false` for a git-describe version, because
  // a source build genuinely cannot be ordered against a release feed -- it may
  // be ahead of the tag it names and behind some later one. Before this, that
  // produced silence, and silence is indistinguishable from "you are current".
  //
  // An operator running a source build is usually doing it deliberately and is
  // exactly the person who wants to know that the update path is not going to
  // tell them anything. So the line explains WHY there is no offer and points
  // at the releases page, which is where the answer actually is.
  //
  // No action button: there is nothing safe to offer. Staging "the latest
  // release" over a build that is ahead of it is the downgrade this whole
  // change exists to stop.
  const devBuild = !info.comparable && !!info.version;

  // Built here rather than in the JSX so the compiler can narrow `info.latest`.
  // A compound guard like `!devBuild && !info.latest` does not narrow it, and
  // reaching for `?? ""` to silence that would put an empty version number in
  // front of an operator rather than admitting the branch cannot happen.
  let headline: string;
  if (devBuild) {
    headline = t("chrome.developmentBuild", { current: info.version });
  } else {
    if (!info.updateAvailable || !info.latest) return null;
    headline = t("chrome.updateAvailable", { latest: info.latest, current: info.version });
  }

  const fail = (e: unknown) => {
    setError(e instanceof Error ? e.message : String(e));
    setStage("idle");
  };

  const prepare = () => {
    setError("");
    setStage("planning");
    api
      .upgradePlan()
      .then((p) => {
        setPlan(p);
        if (!p.automatic) {
          // Docker, a manual install, or a directory this process cannot write
          // to. The server owns the wording; this shows it and stops.
          setStage("manual");
          return;
        }
        if (p.onAirSummary) {
          setStage("confirm");
          return;
        }
        stageUpdate(false);
      })
      .catch(fail);
  };

  const stageUpdate = (force: boolean) => {
    setError("");
    setForced(force);
    setStage("working");
    api
      .upgradeStage(info.latest!, force)
      .then((r) => {
        setResult(r);
        setStage("done");
      })
      .catch(fail);
  };

  const undo = () => {
    setError("");
    setStage("working");
    api
      .upgradeRollback(forced)
      .then((r) => {
        setResult(r);
        setStage("done");
      })
      .catch(fail);
  };

  return (
    <div
      role="status"
      className="flex flex-wrap items-center gap-3 border-b border-border bg-muted/50 px-3 py-1.5 text-sm"
    >
      <span className="min-w-0 flex-1 truncate">
        {headline}
      </span>

      {/* Everything below offers or performs an upgrade, and none of it applies
          to a source build: there is no release that is known to be newer, so
          there is nothing to prepare. The dismiss control stays, because an
          operator who knows what they are running should be able to close it. */}
      {!devBuild && info.onAirSummary && stage === "idle" && (
        // Shown WITH the offer, not instead of it. An operator who learns a
        // release exists is going to act on it eventually, and the useful moment
        // to tell them what is live is while they are deciding -- not after they
        // have clicked something that turned out to end a broadcast.
        <span className="hidden shrink-0 text-muted-foreground sm:inline">
          {t("chrome.updateOnAir", { what: info.onAirSummary })}
        </span>
      )}

      {!devBuild && stage === "idle" && (
        <button type="button" onClick={prepare} className="shrink-0 underline underline-offset-2">
          {t("chrome.updatePrepare")}
        </button>
      )}

      {(stage === "planning" || stage === "working") && (
        <span className="shrink-0 text-muted-foreground">{t("chrome.updatePreparing")}</span>
      )}

      {stage === "confirm" && plan && (
        <span className="flex shrink-0 items-center gap-2">
          <span className="text-muted-foreground">
            {t("chrome.updateInterrupt", { what: plan.onAirSummary ?? "" })}
          </span>
          {/* The override is a separate, explicit click that has already named
              what it will interrupt. There is no setting that makes it the
              default, on purpose. */}
          <button
            type="button"
            onClick={() => stageUpdate(true)}
            className="underline underline-offset-2"
          >
            {t("common.confirm")}
          </button>
          <button
            type="button"
            onClick={() => setStage("idle")}
            className="underline underline-offset-2 text-muted-foreground"
          >
            {t("common.cancel")}
          </button>
        </span>
      )}

      {stage === "manual" && plan && (
        <span className="flex min-w-0 shrink items-center gap-2">
          <span className="truncate text-muted-foreground">
            {plan.reason || t("chrome.updateManual")}
          </span>
          {plan.command && <CopyableCommand value={plan.command} />}
        </span>
      )}

      {stage === "done" && result && (
        <span className="flex min-w-0 shrink items-center gap-2">
          <span className="truncate">
            {result.rolledBack
              ? t("chrome.updateUndone")
              : t("chrome.updateRestartRequired")}
          </span>
          <CopyableCommand value={result.command} />
          {result.staged && (
            // Offered HERE and nowhere else, because here is the only moment it
            // is the right question. A rollback is undoing something that has
            // not taken effect yet; once the service restarts, the banner is
            // gone and recovering from a bad release is a different job.
            <button type="button" onClick={undo} className="underline underline-offset-2">
              {t("chrome.updateUndo")}
            </button>
          )}
        </span>
      )}

      {error && (
        <span className="min-w-0 shrink truncate text-destructive">
          {t("chrome.updateFailed", { error })}
        </span>
      )}

      {info.releaseUrl && (
        <a
          href={info.releaseUrl}
          target="_blank"
          rel="noreferrer noopener"
          className="inline-flex shrink-0 items-center gap-1 underline underline-offset-2"
        >
          {t("chrome.releaseNotes")}
          <ExternalLink className="size-3.5" aria-hidden />
        </a>
      )}
      <button
        type="button"
        onClick={() => setDismissed(true)}
        aria-label={t("chrome.dismiss")}
        className="shrink-0 rounded p-0.5 hover:bg-muted"
      >
        <X className="size-3.5" aria-hidden />
      </button>
    </div>
  );
}

/** A command shown verbatim, with a copy button.
 *
 *  Verbatim matters: these strings are what the operator will paste into a root
 *  shell, so a truncation or a smartened quote is a command that does something
 *  else. The clipboard write is allowed to fail without comment -- it is
 *  blocked on an insecure origin, which a self-hosted box on plain HTTP is --
 *  and the text stays selectable either way. */
function CopyableCommand({ value }: { value: string }) {
  const t = useT();
  const [copied, setCopied] = useState(false);
  return (
    <span className="inline-flex min-w-0 items-center gap-1">
      <code className="truncate rounded bg-background px-1 py-0.5 font-mono text-xs">{value}</code>
      <button
        type="button"
        aria-label={t("common.copy")}
        className="shrink-0 rounded p-0.5 hover:bg-muted"
        onClick={() => {
          navigator.clipboard
            ?.writeText(value)
            .then(() => {
              setCopied(true);
              setTimeout(() => setCopied(false), 1500);
            })
            .catch(() => {});
        }}
      >
        {copied ? <Check className="size-3.5" aria-hidden /> : <Copy className="size-3.5" aria-hidden />}
      </button>
    </span>
  );
}
