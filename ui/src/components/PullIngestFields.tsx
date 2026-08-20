import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { PULL_SCHEMES, RTSP_TRANSPORTS, type PullSettings } from "@/lib/types";
import { useT } from "@/lib/i18n";

/** Mirrors db.DefaultSettings' pull block. Reached only before a source has
 *  ever been given one, or against a server too old to send the key. */
const DEFAULT_PULL: PullSettings = {
  url: "",
  reconnectDelayMaxSeconds: 30,
  rtspTransport: "tcp",
};

/** The three fields a dialled ingest needs.
 *
 *  ONE COPY, rendered by both screens that offer the mode. It used to live
 *  inside SettingsPage, and the Sources page -- which offers the same three
 *  modes in the same select -- rendered nothing at all for `pull`: no URL
 *  field, no reconnect cap, no transport. Choosing it there and pressing Apply
 *  either failed with "pull url is required", naming a field that was not on
 *  the page, or, on a source that already had one stored, silently started
 *  dialling a URL nothing had shown the operator.
 *
 *  A CONTROL rather than a warning: the mistake is not made visible, it is made
 *  impossible. The mode cannot be chosen without the field that gives it
 *  meaning being right there.
 *
 *  The scheme list and the reconnect bounds are the SAME ones the server
 *  enforces, quoted here so a mistake is a hint under the field rather than a
 *  rejected save. They are a hint and nothing more: the server validates
 *  independently, because a UI check is a convenience and never a control.
 *
 *  `idPrefix` because the Sources page draws one of these per source, and two
 *  inputs sharing an id give every label on the page the same target. */
export function PullIngestFields({
  value,
  onChange,
  idPrefix = "pull",
}: {
  value: PullSettings | undefined;
  onChange: (next: PullSettings) => void;
  idPrefix?: string;
}) {
  const t = useT();
  const pull = value ?? DEFAULT_PULL;
  const set = (patch: Partial<PullSettings>) => onChange({ ...pull, ...patch });

  const scheme = pull.url.split("://")[0]?.toLowerCase() ?? "";
  const known = (PULL_SCHEMES as readonly string[]).includes(scheme);

  return (
    <>
      <div className="flex flex-col gap-1">
        <Label htmlFor={`${idPrefix}-url`}>{t("set.sourceUrl")}</Label>
        <Input
          id={`${idPrefix}-url`}
          value={pull.url}
          placeholder={t("set.sourceUrlPlaceholder")}
          onChange={(e) => set({ url: e.target.value })}
        />
        {pull.url !== "" && !known ? (
          <span className="text-[10px] text-warn">
            {t("set.dialsOnly", { schemes: PULL_SCHEMES.join(", ") })}</span>
        ) : (
          <span className="text-[10px] text-muted-foreground">
            One of {PULL_SCHEMES.join(", ")}. A file:// source is a path RELATIVE to the data
            directory and may not contain ".." — it is confined there the same way a file
            destination is.
          </span>
        )}
      </div>

      <div className="grid grid-cols-2 gap-2">
        <div className="flex flex-col gap-1">
          <Label htmlFor={`${idPrefix}-reconnect`}>{t("set.reconnectCap")}</Label>
          <Input
            id={`${idPrefix}-reconnect`}
            type="number"
            value={pull.reconnectDelayMaxSeconds}
            onChange={(e) => set({ reconnectDelayMaxSeconds: Number(e.target.value) })}
          />
          <span className="text-[10px] text-muted-foreground">{t("set.reconnectCapNote")}</span>
        </div>
        <div className="flex flex-col gap-1">
          <Label>{t("set.rtspTransport")}</Label>
          <Select value={pull.rtspTransport || "tcp"} onValueChange={(v) => set({ rtspTransport: v })}>
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {RTSP_TRANSPORTS.map((tr) => (
                <SelectItem key={tr} value={tr}>
                  {tr}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <span className="text-[10px] text-muted-foreground">{t("set.rtspNote")}</span>
        </div>
      </div>
    </>
  );
}
