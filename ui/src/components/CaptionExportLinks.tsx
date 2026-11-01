import { api } from "@/lib/api";
import { useT } from "@/lib/i18n";
import type { CaptionExportFormat, CaptionExportSource } from "@/lib/types";

const FORMATS: readonly CaptionExportFormat[] = ["srt", "vtt", "json"];

/** Caption file downloads for one recording (#8). The transcript row is the
 *  stored transcript, with the programme's caption words applied; the live row
 *  is what viewers saw during the broadcast, lined up with this recording. Both
 *  carry the speaker on each cue. The live row is offered whenever the
 *  recording came from a programme, since only the server knows whether
 *  captions were running; a recording with none answers with a message. */
export function CaptionExportLinks({
  recordingId,
  hasTranscript,
  fromProgramme,
}: Readonly<{ recordingId: number; hasTranscript: boolean; fromProgramme: boolean }>) {
  const t = useT();
  if (!hasTranscript && !fromProgramme) return null;
  const row = (source: CaptionExportSource, label: string) => (
    <div className="flex flex-wrap items-center gap-2" data-testid={`caption-export-${source}`}>
      <span className="text-tiny text-muted-foreground">{label}</span>
      {FORMATS.map((f) => (
        <a
          key={f}
          className="font-mono text-tiny text-primary hover:underline"
          href={api.captionExportUrl(recordingId, f, source)}
          download
        >
          {f.toUpperCase()}
        </a>
      ))}
    </div>
  );
  return (
    <div className="flex flex-col gap-1 rounded border border-border p-2" data-testid="caption-export">
      <span className="text-xs font-medium">{t("wave3.captions.exportTitle")}</span>
      {hasTranscript && row("recording", t("wave3.captions.exportTranscript"))}
      {fromProgramme && row("live", t("wave3.captions.exportLive"))}
      <p className="text-tiny text-muted-foreground">{t("wave3.captions.exportHint")}</p>
    </div>
  );
}
