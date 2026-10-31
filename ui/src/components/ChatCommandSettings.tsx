import { useEffect, useState } from "react";

import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { api } from "@/lib/api";
import { useT } from "@/lib/i18n";
import type { ChatCommandSettings as Value, SourceView } from "@/lib/types";

/** Chat commands (#6): whether chat may run !bitrate, !switch and !fix, and
 *  which programme answers. Chat is install-wide and a command names no
 *  programme, so exactly one programme is under chat's control. */
export function ChatCommandSettings({
  value,
  onChange,
}: {
  value: Value | undefined;
  onChange: (next: Value) => void;
}) {
  const t = useT();
  const v: Value = value ?? { enabled: false };
  // The programme list is a convenience for the picker, not a precondition:
  // a failed read still leaves "default programme" choosable.
  const [sources, setSources] = useState<SourceView[]>([]);
  useEffect(() => {
    let live = true;
    api
      .listSources()
      .then((rows) => {
        if (live) setSources(rows);
      })
      .catch(() => {
        if (live) setSources([]);
      });
    return () => {
      live = false;
    };
  }, []);

  return (
    <Card>
      <CardHeader>
        <CardTitle id="chat-commands">{t("set.chatCommands")}</CardTitle>
        <CardDescription>{t("set.chatCommandsDesc")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <div className="flex items-center justify-between">
          <Label htmlFor="chat-commands-enabled">{t("set.enabled")}</Label>
          <Switch
            id="chat-commands-enabled"
            checked={v.enabled}
            onCheckedChange={(enabled) => onChange({ ...v, enabled })}
          />
        </div>
        <span className="text-sm text-warn">{t("set.chatCommandsWarn")}</span>
        <div className="flex flex-col gap-1">
          <Label>{t("set.chatCommandsProgramme")}</Label>
          <Select
            value={String(v.sourceId ?? 0)}
            onValueChange={(id) => onChange({ ...v, sourceId: Number(id) })}
          >
            <SelectTrigger aria-label={t("set.chatCommandsProgramme")}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="0">{t("set.chatCommandsDefault")}</SelectItem>
              {sources.map((s) => (
                <SelectItem key={String(s.id)} value={String(s.id)}>
                  {s.name}
                </SelectItem>
              ))}
              {/* The stored programme even when the list does not hold it --
                  deleted since, or the list failed to load -- so the picker
                  shows what is configured instead of a blank. */}
              {!!v.sourceId && !sources.some((s) => Number(s.id) === v.sourceId) && (
                <SelectItem value={String(v.sourceId)}>{`#${v.sourceId}`}</SelectItem>
              )}
            </SelectContent>
          </Select>
        </div>
      </CardContent>
    </Card>
  );
}
