import { useId, useState } from "react";
import { toast } from "sonner";
import { Settings2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useT } from "@/lib/i18n";
import { ActivityFeed } from "./ActivityFeed";
import { ActivitySettings } from "./ActivitySettings";
import { ActivitySourceChips } from "./ActivitySourceChips";
import { sendTestAlert } from "./sendTest";
import { useActivity } from "./useActivity";
import { useActivitySources } from "./useActivitySources";

/** Chat -> Activity: source health, the feed, and the settings behind a
 *  disclosure. The feed keeps running while the settings are open, so a test
 *  alert sent from there lands in it the moment the operator goes back. */
export function ActivityTab() {
  const t = useT();
  const feed = useActivity();
  const { sources, failed } = useActivitySources();
  const [settingsOpen, setSettingsOpen] = useState(false);
  const panel = useId();

  const sendDefaultTest = async () => {
    const out = await sendTestAlert(t, "tip");
    if (out.ok) toast.success(out.text);
    else toast.error(out.text);
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-2">
      <div className="flex flex-wrap items-start gap-2">
        <div className="min-w-0 flex-1">
          <ActivitySourceChips data={sources} failed={failed} />
        </div>
        <Button
          size="sm"
          variant={settingsOpen ? "secondary" : "outline"}
          aria-expanded={settingsOpen}
          aria-controls={panel}
          onClick={() => setSettingsOpen((v) => !v)}
        >
          <Settings2 /> {t("activity.settingsToggle")}
        </Button>
      </div>
      {settingsOpen ? (
        <div id={panel} className="min-h-0 flex-1 overflow-y-auto rounded-md border border-border bg-card">
          <ActivitySettings sources={sources} onSaved={() => void feed.reload()} />
        </div>
      ) : (
        <div id={panel} className="flex min-h-0 flex-1 flex-col">
          <ActivityFeed
            events={feed.events}
            loading={feed.loading}
            error={feed.error}
            forbidden={feed.forbidden}
            enabled={feed.enabled}
            connected={feed.connected}
            onReplay={feed.replay}
            onSendTest={() => void sendDefaultTest()}
          />
        </div>
      )}
    </div>
  );
}
