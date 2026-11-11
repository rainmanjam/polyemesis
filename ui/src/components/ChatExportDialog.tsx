import { useState } from "react";
import { Download } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { api } from "@/lib/api";
import {
  CHAT_EXPORT_FORMATS,
  chatExportProblem,
  parseLocalInput,
  toLocalInput,
  type ChatExportFormat,
} from "@/lib/chatExport";
import { useT } from "@/lib/i18n";
import { platformName } from "@/lib/platforms";

/** The Select cannot hold an empty value, so "all platforms" travels as this. */
const ALL = "all";

/* Export chat for a time range, as JSON, CSV or NDJSON.

   The download is a navigation, not a fetch: the server streams the file, and a
   long window would otherwise sit in the browser's memory before it could be
   saved. The window is checked here first so a bad one is a message beside the
   field rather than an error file in the downloads folder. */
export function ChatExportDialog({ platforms }: Readonly<{ platforms: readonly string[] }>) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const [platform, setPlatform] = useState(ALL);
  const [format, setFormat] = useState<ChatExportFormat>("json");
  const [tried, setTried] = useState(false);

  const problem = chatExportProblem(from, to);

  const onOpenChange = (next: boolean) => {
    if (next) {
      // The last two hours, which is what the history keeps by default.
      const now = new Date();
      setFrom(toLocalInput(new Date(now.getTime() - 2 * 3_600_000)));
      setTo(toLocalInput(now));
      setTried(false);
    }
    setOpen(next);
  };

  const start = () => {
    setTried(true);
    const f = parseLocalInput(from);
    if (problem || !f) return;
    const a = document.createElement("a");
    a.href = api.chatExportUrl({
      from: f,
      to: parseLocalInput(to),
      platform: platform === ALL ? "" : platform,
      format,
    });
    a.download = "";
    document.body.appendChild(a);
    a.click();
    a.remove();
    setOpen(false);
  };

  const formatLabel: Record<ChatExportFormat, string> = {
    json: t("chatExport.formatJson"),
    csv: t("chatExport.formatCsv"),
    ndjson: t("chatExport.formatNdjson"),
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogTrigger asChild>
        <Button size="sm" variant="outline">
          <Download aria-hidden="true" className="mr-1 h-3 w-3" />
          {t("chatExport.open")}
        </Button>
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("chatExport.title")}</DialogTitle>
          <DialogDescription>{t("chatExport.desc")}</DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-3">
          <div className="flex flex-wrap gap-3">
            <div className="flex flex-col gap-1">
              <Label htmlFor="chat-export-from">{t("chatExport.from")}</Label>
              <Input
                id="chat-export-from"
                type="datetime-local"
                value={from}
                onChange={(e) => setFrom(e.target.value)}
                aria-invalid={tried && problem === "needFrom"}
              />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor="chat-export-to">{t("chatExport.to")}</Label>
              <Input
                id="chat-export-to"
                type="datetime-local"
                value={to}
                onChange={(e) => setTo(e.target.value)}
                aria-invalid={tried && problem === "needOrder"}
              />
            </div>
          </div>
          {tried && problem && (
            <p role="alert" className="text-sm text-down">
              {t(problem === "needFrom" ? "chatExport.needFrom" : "chatExport.needOrder")}
            </p>
          )}
          <div className="flex flex-wrap gap-3">
            <div className="flex flex-col gap-1">
              <Label htmlFor="chat-export-platform">{t("chatExport.platform")}</Label>
              <Select value={platform} onValueChange={setPlatform}>
                <SelectTrigger id="chat-export-platform" className="w-44">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={ALL}>{t("chatExport.allPlatforms")}</SelectItem>
                  {platforms.map((p) => (
                    <SelectItem key={p} value={p}>
                      {platformName(p)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor="chat-export-format">{t("chatExport.format")}</Label>
              <Select value={format} onValueChange={(v) => setFormat(v as ChatExportFormat)}>
                <SelectTrigger id="chat-export-format" className="w-56">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {CHAT_EXPORT_FORMATS.map((f) => (
                    <SelectItem key={f} value={f}>
                      {formatLabel[f]}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>
          <p className="text-sm text-muted-foreground">{t("chatExport.note")}</p>
        </div>
        <DialogFooter>
          <Button onClick={start}>{t("chatExport.download")}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
