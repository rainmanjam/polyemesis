import { toast } from "sonner";
import { MessageSquare } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { api } from "@/lib/api";
import { downloadChatFile, recordingChatFileName, type RecordingChatFormat } from "@/lib/chatExport";
import { useT } from "@/lib/i18n";
import type { Recording } from "@/lib/types";

/* A recording's chat, as a file to keep next to the video.

   The server writes it beside the recording when the file finishes, so it
   survives the chat purge. A recording that has none (an older one, or one whose
   chat the purge took first) can still be asked for: the server builds it from
   the chat history if that still holds the window, and says so in words if not,
   which the toast repeats. */
export function RecordingChatMenu({ recording }: Readonly<{ recording: Recording }>) {
  const t = useT();

  const save = async (format: RecordingChatFormat) => {
    try {
      await downloadChatFile(
        api.recordingChatUrl(recording.id, format),
        recordingChatFileName(recording.filename, format),
      );
    } catch (e) {
      toast.error(t("rec.chatFailed", { reason: e instanceof Error ? e.message : String(e) }));
    }
  };

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon-sm"
          aria-label={t("rec.chatNamed", { name: recording.filename })}
        >
          <MessageSquare />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="max-w-64">
        <DropdownMenuLabel className="text-tiny font-normal text-muted-foreground">
          {recording.chatSaved ? t("rec.chatSaved") : t("rec.chatNotSaved")}
        </DropdownMenuLabel>
        <DropdownMenuSeparator />
        <DropdownMenuItem onSelect={() => void save("json")}>{t("rec.chatJson")}</DropdownMenuItem>
        <DropdownMenuItem onSelect={() => void save("vtt")}>{t("rec.chatVtt")}</DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
