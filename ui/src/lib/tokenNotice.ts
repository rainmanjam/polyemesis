import type { TranslationKey } from "@/lib/i18n";
import type { IngestMode } from "@/lib/types";

/** Why a source's publish token is not the credential right now, by mode.
 *
 *  The server reports tokenEnforced:false for three different situations, and
 *  the page used to explain all of them with one sentence written for an
 *  install that predated the shared port: "sources are kept apart by port ...
 *  turn on one-port ingest". There is no such setting any more, and the
 *  sentence was shown most often on a source created from its name alone --
 *  whose ingest is not chosen, so the shared SRT port refuses its token
 *  outright. Telling that operator the token is merely "not enforced" invited
 *  them to publish to a source that would refuse them.
 *
 *  - srt / rtmp: the protocol's shared listener is not bound, so nothing is
 *    accepted on it at all;
 *  - pull: polyemesis dials out, no encoder publishes, the token is unused;
 *  - unset (""): nothing is accepted until a protocol is chosen.
 *
 *  The server sends "" for an unchosen ingest even though IngestMode does not
 *  list it, hence the wider parameter.
 */
export function tokenNotEnforcedKey(mode: IngestMode | "" | undefined): TranslationKey {
  switch (mode) {
    case "srt":
      return "sources.tokenNotEnforcedSrt";
    case "rtmp":
      return "sources.tokenNotEnforcedRtmp";
    case "pull":
      return "sources.tokenUnusedPull";
    default:
      return "sources.tokenUnusedUnset";
  }
}
