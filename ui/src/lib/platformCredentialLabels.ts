import type { TranslationKey } from "./i18n";

/** What a platform's credentials card calls its two fields, and where in that
 *  platform's console each one is found.
 *
 *  THE PLATFORM'S WORDS, NOT THE OAUTH SPEC'S. The values are a client_id and
 *  a client_secret on the wire on every platform here, and that is what the API
 *  and the database go on calling them. But an operator copies them out of a
 *  developer console open in the next tab, hunting for a field with the name on
 *  this card -- and Meta's console has no "Client ID" anywhere: App settings →
 *  Basic says "App ID" and "App Secret". Each term below is the one that
 *  platform's console or developer docs print, casing included (Google writes
 *  "Client secret", Twitch "Client Secret").
 *
 *  X IS THE CASE THE HINT EXISTS FOR. Its Keys and tokens page holds the API
 *  Key and Secret (OAuth 1.0a) beside the OAuth 2.0 Client ID and Client
 *  Secret, and only the second pair works here. Its card is not served yet --
 *  the provider is not registered -- but the entry is ready for when it is.
 *
 *  A platform with no entry (Trovo, which is being retired) keeps the generic
 *  pair and no hint, so a new provider is never left with a blank label. */
export interface CredentialLabels {
  id: TranslationKey;
  secret: TranslationKey;
  /** Where the ID is found in the platform's console. Absent for the fallback. */
  idWhere?: TranslationKey;
  /** Where the secret is found. Absent for the fallback. */
  secretWhere?: TranslationKey;
}

const GENERIC: CredentialLabels = { id: "set.clientId", secret: "set.clientSecret" };

const BY_PLATFORM: Readonly<Record<string, CredentialLabels>> = {
  youtube: {
    id: "set.clientId",
    secret: "set.clientSecret",
    idWhere: "credLabels.youtube.idWhere",
    secretWhere: "credLabels.youtube.secretWhere",
  },
  twitch: {
    id: "set.clientId",
    secret: "credLabels.clientSecretCapital",
    idWhere: "credLabels.twitch.idWhere",
    secretWhere: "credLabels.twitch.secretWhere",
  },
  facebook: {
    id: "credLabels.appId",
    secret: "credLabels.appSecret",
    idWhere: "credLabels.facebook.idWhere",
    secretWhere: "credLabels.facebook.secretWhere",
  },
  kick: {
    id: "set.clientId",
    secret: "credLabels.clientSecretCapital",
    idWhere: "credLabels.kick.idWhere",
    secretWhere: "credLabels.kick.secretWhere",
  },
  vimeo: {
    id: "credLabels.clientIdentifier",
    secret: "set.clientSecret",
    idWhere: "credLabels.vimeo.idWhere",
    secretWhere: "credLabels.vimeo.secretWhere",
  },
  x: {
    id: "credLabels.oauth2ClientId",
    secret: "credLabels.clientSecretCapital",
    idWhere: "credLabels.x.idWhere",
    secretWhere: "credLabels.x.secretWhere",
  },
};

export function credentialLabelsFor(platform: string): CredentialLabels {
  return BY_PLATFORM[platform] ?? GENERIC;
}

/** Every field label any card can render. The settings-search drift test reads
 *  this, because the card picks its label at run time and so never writes the
 *  key as a literal the test could find in SettingsPage.tsx. */
export const CREDENTIAL_LABEL_KEYS: readonly TranslationKey[] = [
  ...new Set([GENERIC, ...Object.values(BY_PLATFORM)].flatMap((l) => [l.id, l.secret])),
];
