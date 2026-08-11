import type { TranslationKey } from "@/lib/i18n";

/** Every audience Facebook accepts for a live video, least exposure first.
 *
 *  MIRRORED FROM GO, and the mirror is checked rather than trusted:
 *  `db.FacebookPrivacies` in internal/db/compliance.go is the authority — the
 *  server validates a saved destination against it and returns 400 for anything
 *  else — and internal/db/compliance_drift_test.go pins this array to that one
 *  in both directions. A value added to the Go enum and not to this array fails
 *  `go test ./internal/db/`; a value invented here that Go does not accept fails
 *  the same test, and would otherwise reach an operator as an option that saves
 *  with an unexplained 400.
 *
 *  It lives here, as data, rather than as a hand-written row of `<SelectItem>`s
 *  in DestinationDialog.tsx, for the reason issue #107 records: a guard reading
 *  JSX out of a component cannot tell whether the component renders, so it was
 *  asserting the wrong thing in the wrong language. Split in two, both halves
 *  are honest — Go pins the VALUES against its own enum, and ui/e2e's
 *  facebook-destination.spec.ts drives the real select in a real browser and
 *  proves every value it offers is one the server accepts.
 *
 *  The dialog maps over this array rather than listing the options by hand,
 *  which is what makes the pin worth having: deleting an entry here deletes the
 *  option from the select, so the Go guard fails for exactly the reason an
 *  operator would notice.
 *
 *  `""` (db.FBPrivacyUnchanged) is deliberately absent. It is not an audience;
 *  it is the absence of one, and the select renders it as its own "leave it as
 *  it is on Facebook" row.
 */
export const FACEBOOK_PRIVACIES = [
  { value: "SELF", labelKey: "dest.onlyMe" },
  { value: "ALL_FRIENDS", labelKey: "dest.friends" },
  { value: "FRIENDS_OF_FRIENDS", labelKey: "dest.friendsOfFriends" },
  { value: "EVERYONE", labelKey: "dest.public" },
] as const satisfies readonly { value: string; labelKey: TranslationKey }[];
