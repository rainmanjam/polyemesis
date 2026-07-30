import { useCallback, useEffect, useRef, useState } from "react";
import { Copy, Trash2, Upload, X } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { bytes, timestamp } from "@/lib/format";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { ConfirmDestructive } from "@/components/ConfirmDestructive";
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
