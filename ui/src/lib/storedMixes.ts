import type { RoutingProfile } from "@/lib/types";

/** The mixes a destination holds on the server, as fresh copies (A7).
 *
 *  Discard uses this to put back what is stored. A copy, not the stored object,
 *  so the next edit cannot reach back and change what the next discard reads. An
 *  absent second mix is normalised to null, the same as a destination selection
 *  does, so both paths agree on what "no second mix" means. */
export function storedMixes(dest: {
  profile: RoutingProfile;
  vodProfile?: RoutingProfile | null;
}): { profile: RoutingProfile; vodProfile: RoutingProfile | null } {
  return {
    profile: structuredClone(dest.profile),
    vodProfile: dest.vodProfile ? structuredClone(dest.vodProfile) : null,
  };
}
