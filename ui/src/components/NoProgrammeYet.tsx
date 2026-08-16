import { Link } from "react-router";
import { RadioTower } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { useT } from "@/lib/i18n";

/* The empty state for an install that has no source.
 *
 * ONE COMPONENT, and the reason is the sentence rather than the markup. Three
 * screens meet this state -- the dashboard, the settings ingest tab and the
 * sources page itself -- and the whole point of the server sending
 * `code: "no_source"` is that a client can tell "this install has nothing yet"
 * from "something is broken". Three hand-written cards would drift into three
 * different accounts of the same state, in fifteen languages each, and the
 * operator meeting it for the first time would get a different explanation
 * depending on which tab they happened to open.
 *
 * It is deliberately NOT an error surface. Nothing here is red, nothing says
 * "failed", and there is no retry: a fresh install is working exactly as it
 * should, and the only thing missing is a decision only the operator can make.
 * The red toast this replaced said the opposite, from a 503 that meant nothing
 * of the kind.
 *
 * `action` is the escape hatch for the sources page, which is already AT the
 * destination and needs a button that opens its own dialog rather than a link
 * back to itself.
 */
export function NoProgrammeYet({
  title,
  body,
  action,
}: {
  title: string;
  body: string;
  action?: React.ReactNode;
}) {
  const t = useT();
  return (
    <Card>
      <CardContent className="flex flex-col items-center gap-2 py-8 text-center">
        <RadioTower className="h-5 w-5 text-muted-foreground" aria-hidden />
        <p className="text-[13px] font-semibold tracking-tight">{title}</p>
        <p className="max-w-prose text-[12px] text-muted-foreground">{body}</p>
        {action ?? (
          <Button asChild size="sm">
            <Link to="/sources">{t("empty.goToSources")}</Link>
          </Button>
        )}
      </CardContent>
    </Card>
  );
}
