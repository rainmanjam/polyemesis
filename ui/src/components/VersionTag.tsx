import type { VersionInfo } from "../lib/types";

import { useT } from "@/lib/i18n";

/** The running version, in the chrome, where an operator can always see it.
 *
 *  It was shown NOWHERE on a healthy install. UpdateBanner is the only thing
 *  that reads info.version, and it returns null unless an update is available
 *  or the build is a development one -- so the normal state, an up-to-date
 *  release, displayed no version at all. Every support question and every "is
 *  the fix deployed yet?" starts with which version this is, and answering it
 *  required SSH. #660.
 *
 *  Deliberately quiet. This is reference information an operator looks up, not
 *  a status competing with the live indicators beside it, so it takes the same
 *  muted treatment as the username it sits next to and carries no colour of its
 *  own. It is not a second update banner: it says what is running and nothing
 *  about whether that is current.
 *
 *  Renders nothing until the answer arrives, rather than a placeholder. An
 *  em-dash where a version belongs invites the reader to conclude something
 *  about their install from a fact nobody has stated yet -- which is #663 in
 *  miniature, and the reason this waits instead.
 */
export function VersionTag({ info }: { info: VersionInfo | null }) {
  const t = useT();
  if (!info?.version) return null;

  // A git-describe build is reported as not comparable, and it is shown as-is:
  // "which build is this" is exactly the question being answered, and a
  // development build is a legitimate answer to it.
  return (
    <span
      className="hidden text-[11px] text-muted-foreground xl:inline"
      title={t("chrome.runningVersion", { version: info.version })}
    >
      {info.version}
    </span>
  );
}
