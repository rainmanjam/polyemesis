import { useCallback, useEffect, useRef, useState } from "react";
import { Copy, ShieldAlert, ShieldX, Trash2, Upload, X } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { bytes, duration, timestamp } from "@/lib/format";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { ConfirmDestructive } from "@/components/ConfirmDestructive";
import { uploadNotice } from "@/lib/upload-verdict";
import type { MediaFile } from "@/lib/types";

/* ===========================================================================
   Upload a file to the server, and see what is already there.

   Before this, going live from a file meant copying it onto the box yourself:
   fine on a Linux host you already have a session on, a wall for everyone
   running the container.

   Two things this deliberately shows that a plainer uploader would not:

   - the ORIGIN of every file, so an operator can tell at a glance what the
     server captured from a stream and what they put there themselves. That
     distinction is load-bearing rather than cosmetic: retention sweeps
     recordings and never touches uploads, so it is the difference between a
     file that will disappear on a policy and one that is the only copy.

   - the PULL URL, copyable, because an uploaded file is worth nothing until it
     is pointed at. Uploading and then leaving the operator to guess the path
     would be a feature that stops one step short of useful.
   =========================================================================== */

/** Progress for one in-flight upload. */
interface InFlight {
  name: string;
  fraction: number;
  controller: AbortController;
}

export function MediaUploads() {
  const [files, setFiles] = useState<MediaFile[]>([]);
  const [inFlight, setInFlight] = useState<InFlight[]>([]);
  const [error, setError] = useState("");
  const [dragging, setDragging] = useState(false);
  const [copied, setCopied] = useState("");
  const [pendingDelete, setPendingDelete] = useState<MediaFile | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);

  const refresh = useCallback(async () => {
    try {
      setFiles(await api.media());
    } catch (e) {
      setError(e instanceof ApiError ? e.message : String(e));
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const upload = useCallback(
    async (list: FileList | File[]) => {
      setError("");
      for (const file of Array.from(list)) {
        const controller = new AbortController();
        const entry: InFlight = { name: file.name, fraction: 0, controller };
        setInFlight((cur) => [...cur, entry]);
        try {
          await api.uploadMedia(
            file,
            (fraction) =>
              setInFlight((cur) =>
                cur.map((f) => (f.controller === controller ? { ...f, fraction } : f)),
              ),
            controller.signal,
          );
        } catch (e) {
          // Named, not swallowed. An upload that fails silently leaves the
          // operator believing a file is on the server when it is not, and
          // they find out when the broadcast they scheduled goes to air.
          setError(
            `${file.name}: ${e instanceof ApiError ? e.message : String(e)}`,
          );
        } finally {
          setInFlight((cur) => cur.filter((f) => f.controller !== controller));
        }
      }
      void refresh();
    },
    [refresh],
  );

  const onDrop = useCallback(
    (e: React.DragEvent) => {
      e.preventDefault();
      setDragging(false);
      if (e.dataTransfer.files.length) void upload(e.dataTransfer.files);
    },
    [upload],
  );

  async function copyPullUrl(f: MediaFile) {
    await navigator.clipboard.writeText(f.pullUrl);
    setCopied(f.name);
    window.setTimeout(() => setCopied(""), 2000);
  }

  async function remove(f: MediaFile) {
    try {
      await api.deleteMedia(f.name);
      void refresh();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : String(e));
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>Media</CardTitle>
        <CardDescription>
          Upload a file to broadcast from it on a schedule, with no encoder
          attached. Copy its pull URL into <em>Settings → Ingest → Pull</em>.
        </CardDescription>
      </CardHeader>

      <CardContent className="grid gap-3">
        {/* The drop zone doubles as a button: drag-and-drop is undiscoverable
            on its own, and unreachable by keyboard. */}
        <div
          onDragOver={(e) => {
            e.preventDefault();
            setDragging(true);
          }}
          onDragLeave={() => setDragging(false)}
          onDrop={onDrop}
          onClick={() => inputRef.current?.click()}
          onKeyDown={(e) => {
            if (e.key === "Enter" || e.key === " ") inputRef.current?.click();
          }}
          role="button"
          tabIndex={0}
          aria-label="Upload a media file"
          className={[
            "flex cursor-pointer flex-col items-center justify-center gap-2",
            "rounded-md border border-dashed p-6 text-center transition-colors",
            dragging ? "border-primary bg-primary/5" : "border-border",
          ].join(" ")}
        >
          <Upload className="size-5 opacity-70" aria-hidden />
          <div className="text-sm">
            Drop a file here, or <span className="underline">choose one</span>
          </div>
          <div className="text-xs text-muted-foreground">
            MPEG-TS is the least surprising container. An MP4 works, but its
            moov atom makes FFmpeg want the whole file before it starts.
          </div>
        </div>
        <input
          ref={inputRef}
          type="file"
          multiple
          className="hidden"
          onChange={(e) => {
            if (e.target.files?.length) void upload(e.target.files);
            // Reset so choosing the SAME file twice still fires a change event.
            e.target.value = "";
          }}
        />

        {inFlight.map((f) => (
          <div key={f.name + f.fraction} className="grid gap-1">
            <div className="flex items-center justify-between text-xs">
              <span className="truncate">{f.name}</span>
              <div className="flex items-center gap-2">
                <span className="font-mono">{Math.round(f.fraction * 100)}%</span>
                <Button
                  size="icon"
                  variant="ghost"
                  className="size-6"
                  aria-label={`Cancel uploading ${f.name}`}
                  onClick={() => f.controller.abort()}
                >
                  <X className="size-3" />
                </Button>
              </div>
            </div>
            <div className="h-1 w-full overflow-hidden rounded bg-muted">
              <div
                className="h-full bg-primary transition-[width]"
                style={{ width: `${f.fraction * 100}%` }}
              />
            </div>
          </div>
        ))}

        {error && (
          <div className="rounded-md border border-destructive/40 bg-destructive/10 p-2 text-xs text-destructive">
            {error}
          </div>
        )}

        {files.length === 0 && inFlight.length === 0 ? (
          <div className="text-xs text-muted-foreground">
            Nothing uploaded yet.
          </div>
        ) : (
          <div className="grid gap-1">
            {files.map((f) => (
              <div
                key={f.name}
                className="flex items-center justify-between gap-2 rounded-md border p-2"
              >
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    {/* The origin tag. Uploaded files are never swept by
                        retention; recordings are. */}
                    <span className="rounded bg-muted px-1.5 py-0.5 text-[10px] uppercase tracking-wide text-muted-foreground">
                      {f.origin}
                    </span>
                    <span className="truncate font-mono text-xs">{f.name}</span>
                  </div>
                  <div className="mt-0.5 text-[11px] text-muted-foreground">
                    {bytes(f.bytes)} · {timestamp(f.modified)}
                  </div>
                  {/* THE THIRD STATE, said out loud.
                      An upload can be stored WITHOUT having been inspected —
                      the server's check runs while the request is open, so a
                      connection that drops after the bytes have landed cuts it
                      short, and a check that did not finish is not a verdict
                      about the file. The bytes are kept, which is right. What
                      was wrong is that the result looked exactly like a file
                      that had passed: `media` was simply absent, which is also
                      how every upload from before the check existed looks.
                      A row that says nothing here is a row that lets an
                      operator schedule a file nobody has read. */}
                  {/* REFUSED IS NOT "NOT CHECKED", and this row is the whole
                      reason the third state exists. A file that was inspected
                      and rejected is the opposite of an unchecked one: the
                      server read it and knows exactly what it is. Rendering
                      both as "Not checked" would state something the server
                      knows to be false, and — worse — would hand the operator
                      the one remedy that cannot work, since re-sending the same
                      bytes earns the same refusal. Nothing writes this state
                      yet (see #202: the re-verify job is what will), and the
                      row is built first so that when something does, the
                      Library does not lie about it for a release.
                      The wording lives in lib/upload-verdict so that this row
                      and the playlist editor's filter cannot disagree. */}
                  {(() => {
                    const notice = uploadNotice(f);
                    if (!notice) return null;
                    const Icon =
                      notice.tone === "refused" ? ShieldX : ShieldAlert;
                    return (
                      <div
                        className={`mt-0.5 flex items-center gap-1 text-[11px] ${
                          notice.tone === "refused"
                            ? "text-destructive"
                            : "text-warn"
                        }`}
                        title={notice.detail}
                      >
                        <Icon className="size-3 shrink-0" aria-hidden />
                        <span>{notice.label}</span>
                      </div>
                    );
                  })()}
                  {/* What the file actually is, which a name and a size cannot
                      say. The track count leads because routing is per track:
                      selecting track 3 of a file that carries one is silence
                      on air, and this is the only place to notice beforehand.
                      Absent for anything stored before uploads were probed,
                      which renders as nothing rather than as zeroes. */}
                  {f.media && (
                    <div className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-[11px] text-muted-foreground">
                      {f.media.durationSeconds > 0 && (
                        <span>{duration(f.media.durationSeconds)}</span>
                      )}
                      {f.media.width > 0 && (
                        <span>
                          {f.media.width}×{f.media.height}
                          {f.media.frameRate > 0 && ` @ ${f.media.frameRate.toFixed(0)}fps`}
                        </span>
                      )}
                      {f.media.videoCodec && <span>{f.media.videoCodec}</span>}
                      <span
                        className={
                          f.media.audioTracks === 0 ? "text-warn" : undefined
                        }
                        title={
                          f.media.audioTracks === 0
                            ? "No audio. Every major platform refuses a video-only stream — turn on the silence tier in Settings → Synthetic."
                            : undefined
                        }
                      >
                        {f.media.audioTracks} audio{" "}
                        {f.media.audioTracks === 1 ? "track" : "tracks"}
                        {f.media.audioLayout ? ` (${f.media.audioLayout})` : ""}
                      </span>
                    </div>
                  )}
                </div>
                <div className="flex shrink-0 items-center gap-1">
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => void copyPullUrl(f)}
                    aria-label={`Copy the pull URL for ${f.name}`}
                  >
                    <Copy className="size-3.5" />
                    {copied === f.name ? "Copied" : "Pull URL"}
                  </Button>
                  <Button
                    size="icon"
                    variant="ghost"
                    className="size-8"
                    onClick={() => setPendingDelete(f)}
                    aria-label={`Delete ${f.name}`}
                  >
                    <Trash2 className="size-3.5" />
                  </Button>
                </div>
              </div>
            ))}
          </div>
        )}
      </CardContent>

      {/* Deleting an upload is irreversible and it may be the only copy: the
          server did not produce this file and cannot recreate it. So it goes
          through the same confirmation as every other destructive action
          rather than a bare button. */}
      {pendingDelete && (
        <ConfirmDestructive
          open
          onOpenChange={(o) => {
            if (!o) setPendingDelete(null);
          }}
          subject={pendingDelete.name}
          title="Delete this upload?"
          // requireTyping, because this one genuinely does not come back and
          // may be the only copy: polyemesis did not produce this file and
          // cannot recreate it, unlike a recording it could in principle
          // capture again. That is the distinction the flag exists for.
          requireTyping
          description="polyemesis did not create this file and cannot recover it. If this is your only copy, it is gone."
          confirmLabel="Delete"
          onConfirm={() => {
            const f = pendingDelete;
            setPendingDelete(null);
            void remove(f);
          }}
        />
      )}
    </Card>
  );
}
