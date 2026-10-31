import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router";
import { toast } from "sonner";
import { AlertTriangle, Bot, Loader2 } from "lucide-react";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { api } from "@/lib/api";
import { useT, type Translator, type TranslationKey } from "@/lib/i18n";
import type { McpAccess, McpLevel } from "@/lib/types";

const LEVELS: { level: McpLevel; label: TranslationKey; desc: TranslationKey; warning?: TranslationKey }[] = [
  { level: "read", label: "mcp.levelRead", desc: "mcp.levelReadDesc" },
  { level: "content", label: "mcp.levelContent", desc: "mcp.levelContentDesc", warning: "mcp.contentWarning" },
  { level: "control", label: "mcp.levelControl", desc: "mcp.levelControlDesc", warning: "mcp.controlWarning" },
];

const LEVEL_LABEL: Record<McpLevel, TranslationKey> = {
  read: "mcp.levelRead",
  content: "mcp.levelContent",
  control: "mcp.levelControl",
};

/** "MCP: off", or "MCP: on (Read + Control)". The one line the card and the
 *  System page both show. */
export function mcpStatusText(t: Translator, a: Pick<McpAccess, "enabled" | "levels">): string {
  if (!a.enabled) return t("mcp.statusOff");
  if (a.levels.length === 0) return t("mcp.statusNoLevels");
  return t("mcp.statusOn", { levels: a.levels.map((l) => t(LEVEL_LABEL[l])).join(" + ") });
}

/** AI assistant access (MCP): the switch, the three levels, and where to get
 *  a token.
 *
 *  Everything here is ENFORCED BY THE SERVER, which is why the card can be this
 *  plain: a level that is off is not offered to any token, and no API token can
 *  change these settings -- the route is session-only. The card only says what
 *  each choice means, and warns where a choice lets text written by viewers
 *  reach an assistant that can act. */
export function McpAccessCard() {
  const t = useT();
  const [access, setAccess] = useState<McpAccess | null>(null);
  const [failed, setFailed] = useState(false);
  const [saving, setSaving] = useState(false);

  const load = useCallback(async () => {
    try {
      setAccess(await api.mcpAccess());
      setFailed(false);
    } catch {
      setFailed(true);
    }
  }, []);

  useEffect(() => {
    load().catch(() => setFailed(true));
  }, [load]);

  const save = async (enabled: boolean, levels?: McpLevel[]) => {
    setSaving(true);
    try {
      setAccess(await api.setMcpAccess(enabled, levels));
    } catch (e) {
      toast.error(e instanceof Error && e.message ? e.message : t("mcp.saveFailed"));
    } finally {
      setSaving(false);
    }
  };

  const toggleLevel = (level: McpLevel, on: boolean) => {
    if (!access) return;
    const next = on ? [...access.levels, level] : access.levels.filter((l) => l !== level);
    // In the server's order, so the status line reads the same either way.
    void save(true, access.allLevels.filter((l) => next.includes(l)));
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <Bot className="h-4 w-4" /> {t("mcp.title")}
        </CardTitle>
        <CardDescription>{t("mcp.desc")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {failed && <span className="text-tiny text-down">{t("mcp.loadFailed")}</span>}
        {!access && !failed && <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />}
        {access && (
          <>
            <div className="flex items-center gap-2">
              <Switch
                id="mcp-enabled"
                checked={access.enabled}
                disabled={saving}
                onCheckedChange={(v) => void save(Boolean(v))}
              />
              <Label htmlFor="mcp-enabled">{t("mcp.enable")}</Label>
            </div>
            <span data-testid="mcp-status" className="text-tiny font-medium">
              {mcpStatusText(t, access)}
            </span>

            <div className="flex flex-col gap-2">
              {LEVELS.map(({ level, label, desc, warning }) => {
                const on = access.enabled && access.levels.includes(level);
                return (
                  <div key={level} className="flex flex-col gap-1 rounded border border-border p-2">
                    <div className="flex items-center gap-2">
                      <Checkbox
                        id={`mcp-level-${level}`}
                        checked={on}
                        disabled={!access.enabled || saving}
                        onCheckedChange={(v) => toggleLevel(level, Boolean(v))}
                      />
                      <Label htmlFor={`mcp-level-${level}`}>{t(label)}</Label>
                    </div>
                    <span className="text-sm text-muted-foreground">{t(desc)}</span>
                    {warning && (
                      <span className="flex items-start gap-1 text-sm text-warn">
                        <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0" /> {t(warning)}
                      </span>
                    )}
                  </div>
                );
              })}
            </div>

            <div className="flex flex-col gap-1">
              <Link
                to="/settings?tab=security"
                className="self-start text-tiny underline underline-offset-2 hover:text-foreground"
              >
                {t("mcp.createToken")}
              </Link>
              <span className="text-sm text-muted-foreground">{t("mcp.tokenHint")}</span>
            </div>
          </>
        )}
      </CardContent>
    </Card>
  );
}

/** The status line alone, for the System page. Reads the same session-only
 *  route; says nothing if it cannot. */
export function McpStatusLine() {
  const t = useT();
  const [access, setAccess] = useState<McpAccess | null>(null);
  useEffect(() => {
    // A failed read shows nothing rather than a claim: the line is a pointer
    // to the card, which says when it could not read the settings.
    api
      .mcpAccess()
      .then(setAccess)
      .catch(() => undefined);
  }, []);
  if (!access) return null;
  return (
    <Link to="/automation?tab=mcp" data-testid="mcp-status-line" className="text-tiny hover:underline">
      {mcpStatusText(t, access)}
    </Link>
  );
}
