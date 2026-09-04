import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { useLiveData } from "@/hooks/useLiveData";
import { useT } from "@/lib/i18n";

/** #638: WHICH PROGRAMME THE CONSOLE IS FOLLOWING, AND THE MEANS TO CHANGE IT.
 *
 *  The console has always followed exactly one programme, chosen by
 *  resolveProgramme: whatever was last looked at, else the server's first
 *  source. `rememberProgramme` existed to persist that choice and was called
 *  from exactly one place — with the value the resolver had just picked. So the
 *  console remembered its own default, forever, and no screen offered a way to
 *  say otherwise.
 *
 *  On a single-programme install that was invisible and harmless. On one with a
 *  horizontal and a vertical, an operator watched one programme's meters, one
 *  programme's destinations and one programme's levels believing they covered
 *  the install, while the other ran unwatched with nothing on screen indicating
 *  it existed. The meters page is where it bit hardest — its entire purpose is
 *  "watch track 1 move and track 2 stay flat" — which is why that page already
 *  names the programme it is showing. Naming turns a silent wrong answer into a
 *  visibly partial one; this is the half that makes it answerable.
 *
 *  HIDDEN WHEN THERE IS NOTHING TO CHOOSE. A control that offers one option is
 *  furniture, and worse than furniture in the chrome, where it would occupy a
 *  fixed slot on every screen of every single-source install to say nothing.
 *  The meters badge takes the same view for the same reason.
 */
export function ProgrammeSwitcher() {
  const t = useT();
  const { programmes, programme, programmeKnown, selectProgramme } = useLiveData();

  // Gate on programmeKnown, not on `programme != null`. Null means EITHER "no
  // sources" or "not resolved yet", and rendering a picker during the second
  // gives an operator a control that silently does nothing — the shape #606
  // kept coming back as.
  if (!programmeKnown || programmes.length < 2) return null;

  return (
    <Select
      value={programme == null ? "" : String(programme)}
      onValueChange={(v) => selectProgramme(Number(v))}
    >
      <SelectTrigger
        aria-label={t("chrome.programme")}
        title={t("chrome.programmeHint")}
        data-testid="programme-switcher"
        className="h-7 max-w-[10rem] text-[11px]"
      >
        <SelectValue placeholder={t("chrome.programme")} />
      </SelectTrigger>
      <SelectContent>
        {programmes.map((p) => (
          <SelectItem key={p.id} value={String(p.id)}>
            {p.name}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}
