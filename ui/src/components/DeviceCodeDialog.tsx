import { useCallback, useEffect, useRef, useState } from "react";
import { Check, Copy, ExternalLink, Loader2 } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { api } from "@/lib/api";
import {
  DEVICE_POLL_FLOOR_MS,
  deviceCountdown,
  deviceFlowAfterPoll,
  deviceFlowIsPolling,
  deviceHasExpired,
  devicePollDelayMs,
  deviceSecondsRemaining,
  type DeviceFlowPhase,
} from "@/lib/deviceFlow";
import { useT } from "@/lib/i18n";

/* ===========================================================================
   Connecting an account from a box no platform can redirect back to.

   The ordinary Connect button sends the browser to the platform's consent
   screen and the platform sends it back — to a redirect URI the platform has to
   accept. polyemesis builds that URI from the request it is answering, so an
   operator reaching this UI as https://192.168.1.50, or on a self-signed
   certificate, has no callback any platform will match and the Connect button
   simply cannot work for them. That is who this dialog is for.

   THE CODE IS THE PRODUCT AND EVERY DECISION HERE FOLLOWS FROM IT. The operator
   is going to read eight characters off this screen and type them into a phone,
   probably while standing up. So the code is the biggest thing in the dialog,
   in a monospace face where 0/O and 1/l are distinguishable, letter-spaced so a
   double character cannot be miscounted, and selectable — with a copy button
   for the case where the phone is not the second device. A code rendered at
   body size in the UI font is a support ticket about "the platform says my code
   is wrong".

   AND IT SAYS WHEN IT WILL DIE. A device code expires — half an hour, on the one
   platform that offers this — and a dialog that polls silently past that point
   shows a spinner that will never resolve. The countdown is not decoration: the
   client stops on it, before the server has to.
   =========================================================================== */

export interface DeviceCodeDialogProps {
  /** The platform being connected, as the API spells it. */
  platform: string;
  /** The operator-facing name, for the sentences. */
  platformName: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Called once, after an account is connected, so the caller can reload its
   *  list. Not called on cancel or expiry — nothing changed. */
  onConnected: () => void;
}

export function DeviceCodeDialog({
  platform,
  platformName,
  open,
  onOpenChange,
  onConnected,
}: Readonly<DeviceCodeDialogProps>) {
  const t = useT();
  const [phase, setPhase] = useState<DeviceFlowPhase>({ kind: "idle" });
  const [remaining, setRemaining] = useState<number | null>(null);

  const start = useCallback(async () => {
    setPhase({ kind: "starting" });
    try {
      const auth = await api.startDeviceAuth(platform);
      setPhase({ kind: "waiting", auth });
    } catch (err) {
      // The server's own sentence, verbatim. It names the platform, and for the
      // two cases an operator can actually fix — no credentials stored, or a
      // platform with no device flow at all — the fix is IN that sentence.
      setPhase({
        kind: "failed",
        reason: err instanceof Error ? err.message : t("device.startFailed"),
      });
    }
  }, [platform, t]);

  // Opening the dialog IS the request. There is no second button to press:
  // the operator already pressed one, and a dialog whose first state is another
  // button is a dialog that wasted a click.
  useEffect(() => {
    if (open) void start();
    else setPhase({ kind: "idle" });
  }, [open, start]);

  // THE POLL. Rescheduled after each answer rather than run on a fixed
  // interval, because the wait is the SERVER'S to choose — it may widen it —
  // and because a fixed interval stacks requests behind a slow one, which is
  // the request storm this whole feature has to avoid against the operator's
  // own developer app.
  const phaseRef = useRef(phase);
  phaseRef.current = phase;
  useEffect(() => {
    if (!deviceFlowIsPolling(phase) || phase.kind !== "waiting") return;
    const handle = phase.auth.handle;
    let cancelled = false;
    let timer = 0;

    const tick = async () => {
      if (cancelled) return;
      // Stopped on the CLIENT'S clock as well as the server's. An expired code
      // can only ever answer "invalid device code", so asking is a request
      // spent on a foregone conclusion.
      if (deviceHasExpired(phase.auth.expiresAt, Date.now())) {
        setPhase({ kind: "failed", reason: t("device.expired") });
        return;
      }
      try {
        const res = await api.pollDeviceAuth(platform, handle);
        if (cancelled) return;
        setPhase((prev) => deviceFlowAfterPoll(prev, res));
        // Read back through the ref rather than the closure: the state that
        // decides whether to keep going is the one the reducer just produced.
        if (phaseRef.current.kind === "waiting" || res.state === "pending") {
          timer = window.setTimeout(
            () => void tick(),
            devicePollDelayMs(res.retryInSeconds ?? phase.auth.intervalSeconds),
          );
        }
      } catch {
        // A transport failure is NOT the end of the flow. The code is still
        // good and the operator may still be typing, so this keeps waiting on
        // the floor interval rather than tearing the dialog down over one bad
        // response. The countdown above is what eventually stops it.
        if (!cancelled) timer = window.setTimeout(() => void tick(), DEVICE_POLL_FLOOR_MS);
      }
    };

    timer = window.setTimeout(() => void tick(), devicePollDelayMs(phase.auth.intervalSeconds));
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [phase, platform, t]);

  // The countdown ticks independently of the poll, so the number moves once a
  // second rather than once every five.
  useEffect(() => {
    if (phase.kind !== "waiting") {
      setRemaining(null);
      return;
    }
    const read = () => setRemaining(deviceSecondsRemaining(phase.auth.expiresAt, Date.now()));
    read();
    const id = window.setInterval(read, 1000);
    return () => window.clearInterval(id);
  }, [phase]);

  // Success closes the dialog and tells the caller, once. A "connected" screen
  // the operator has to dismiss is a screen between them and the thing they
  // came here to do.
  const settled = useRef(false);
  useEffect(() => {
    if (phase.kind !== "connected") {
      settled.current = false;
      return;
    }
    if (settled.current) return;
    settled.current = true;
    toast.success(t("device.connected", { account: phase.accountName || platformName }));
    onConnected();
    onOpenChange(false);
  }, [phase, onConnected, onOpenChange, platformName, t]);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t("device.title", { platform: platformName })}</DialogTitle>
          <DialogDescription>
            {/* Long explanatory prose beside a control stays literal English,
                per the convention DestinationDialog sets. The labels, buttons
                and toasts around it are catalogue keys. */}
            Type this code on another device — a phone works. This box never
            needs a public address, which is the whole point of connecting this
            way.
          </DialogDescription>
        </DialogHeader>

        {phase.kind === "starting" && (
          <div className="flex items-center gap-2 py-6 text-sm text-muted-foreground">
            <Loader2 className="size-4 animate-spin" aria-hidden />
            {t("device.starting")}
          </div>
        )}

        {phase.kind === "waiting" && (
          <div className="flex flex-col gap-3">
            <UserCode code={phase.auth.userCode} />

            <div className="flex flex-col gap-1.5">
              <Button asChild variant="outline" size="sm">
                <a href={phase.auth.verificationUri} target="_blank" rel="noreferrer noopener">
                  <ExternalLink aria-hidden /> {t("device.openPage")}
                </a>
              </Button>
              {/* The address in full as well as behind the button: the second
                  device is usually not the one showing this dialog, so it has
                  to be readable and typeable, not only clickable. */}
              <code className="break-all text-center text-[10px] text-muted-foreground">
                {phase.auth.verificationUri}
              </code>
            </div>

            <div className="flex items-center justify-center gap-2 text-[11px] text-muted-foreground">
              <Loader2 className="size-3 animate-spin" aria-hidden />
              <span>{t("device.waiting")}</span>
              {remaining !== null && (
                <span className="font-mono">{deviceCountdown(remaining)}</span>
              )}
            </div>
          </div>
        )}

        {phase.kind === "failed" && (
          <div className="flex flex-col gap-2 py-2">
            <p className="text-sm text-warn">{phase.reason || t("device.expired")}</p>
          </div>
        )}

        <DialogFooter>
          {phase.kind === "failed" && (
            <Button size="sm" variant="outline" onClick={() => void start()}>
              {t("device.tryAgain")}
            </Button>
          )}
          <Button size="sm" variant="ghost" onClick={() => onOpenChange(false)}>
            {t("common.cancel")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/** The code itself. See the header for why it is rendered like this. */
function UserCode({ code }: { code: string }) {
  const t = useT();
  const [copied, setCopied] = useState(false);
  return (
    <div className="flex items-center justify-center gap-2 rounded border border-border bg-background px-3 py-4">
      <code
        className="select-all font-mono text-2xl font-semibold tracking-[0.25em]"
        // Read aloud as characters rather than as a word: a screen reader
        // pronouncing "ABCD-1234" as a syllable gives the operator nothing to
        // type.
        aria-label={code.split("").join(" ")}
      >
        {code}
      </code>
      <button
        type="button"
        aria-label={t("common.copy")}
        className="shrink-0 rounded p-1 hover:bg-muted"
        onClick={() => {
          // Allowed to fail without comment, exactly as UpdateBanner's copy
          // does: the clipboard is blocked on an insecure origin, which a
          // self-hosted box on plain HTTP is — and which is the same population
          // this whole feature exists for. The code stays selectable.
          navigator.clipboard
            ?.writeText(code)
            .then(() => {
              setCopied(true);
              setTimeout(() => setCopied(false), 1500);
            })
            .catch(() => {});
        }}
      >
        {copied ? <Check className="size-4" aria-hidden /> : <Copy className="size-4" aria-hidden />}
      </button>
    </div>
  );
}
