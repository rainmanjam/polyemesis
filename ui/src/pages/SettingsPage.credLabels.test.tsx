// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

import { PlatformCredCard } from "./SettingsPage";
import type { Platform, SetupGuide } from "@/lib/types";

/* EACH CARD NAMES ITS TWO FIELDS THE WAY THE PLATFORM'S OWN CONSOLE DOES.
 *
 * Every card used to say "Client ID" and "Client secret", whatever the
 * platform. That is the OAuth spec's vocabulary, and no operator reads the
 * spec: they have the platform's developer console open in the next tab and
 * are looking for a field with the same name. On Facebook there is none --
 * Meta's App settings → Basic page says "App ID" and "App Secret" -- so the
 * operator who reported this went looking for a "Client ID" that Meta's page
 * never shows.
 *
 * X is the sharper case. Its Keys and tokens page holds TWO pairs: the API Key
 * and Secret (OAuth 1.0a, which polyemesis does not speak) and the OAuth 2.0
 * Client ID and Client Secret (which it needs). Pasting the first pair is the
 * natural mistake, and the label plus the hint under it are what prevent it.
 * No X card renders yet -- the provider exists but is not registered -- so the
 * guide here is synthetic, and the case pins the label for the day it is.
 *
 * The values stay `clientId` / `clientSecret` on the wire. Only the words
 * change, which is why the last case checks a platform with no entry still
 * gets the generic pair rather than a blank label. */

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: {
      connectUrl: (p: string) => `/api/v1/oauth/${p}/start`,
      putCreds: vi.fn(),
      deleteCreds: vi.fn(),
      checkCreds: vi.fn(),
      deleteAccount: vi.fn(),
      confirmDeleteAccount: vi.fn(),
      accountStats: vi.fn(async () => ({ supported: false, reason: "not in this test" })),
    },
  };
});

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() } }));

function guideFor(platform: string, name: string): SetupGuide {
  return {
    // "x" is not in the Platform union yet: see the header.
    platform: platform as Platform,
    name,
    consoleUrl: "",
    redirectPath: `/api/v1/oauth/${platform}/callback`,
    steps: [],
    scopes: null,
    supported: true,
  };
}

function renderCard(platform: string, name: string) {
  return render(<PlatformCredCard guide={guideFor(platform, name)} creds={null} accounts={[]} onChanged={vi.fn()} />);
}

/** The hint text an input is described by, which is how a screen reader
 *  hears "where do I find this" after the label itself. */
function hintOf(el: HTMLElement): string {
  const ids = el.getAttribute("aria-describedby") ?? "";
  return ids
    .split(/\s+/)
    .filter(Boolean)
    .map((id) => document.getElementById(id)?.textContent ?? "")
    .join(" ");
}

afterEach(cleanup);

describe("platform credential fields use the console's own words", () => {
  it("labels Facebook's pair App ID and App Secret, pointing at App settings → Basic", () => {
    renderCard("facebook", "Facebook Live (Meta)");

    const id = screen.getByLabelText("App ID");
    const secret = screen.getByLabelText("App Secret");
    expect(screen.queryByLabelText("Client ID")).toBeNull();
    expect(screen.queryByLabelText("Client secret")).toBeNull();

    expect(hintOf(id)).toContain("App settings → Basic");
    expect(hintOf(secret)).toContain("App settings → Basic");
  });

  it("labels X's pair as the OAuth 2.0 Client ID and warns off the OAuth 1.0a API Key", () => {
    renderCard("x", "X");

    const id = screen.getByLabelText("OAuth 2.0 Client ID");
    const secret = screen.getByLabelText("Client Secret");
    expect(hintOf(id)).toContain("Keys and tokens");
    // The distinction the operator most needs: which of X's two pairs.
    expect(hintOf(secret)).toContain("API Key");
    expect(hintOf(secret)).toContain("OAuth 1.0a");
  });

  it.each([
    ["youtube", "YouTube (Google)", "Client ID", "Client secret", "Clients"],
    ["twitch", "Twitch", "Client ID", "Client Secret", "New Secret"],
    ["kick", "Kick", "Client ID", "Client Secret", "Developer"],
    ["vimeo", "Vimeo", "Client identifier", "Client secret", "Manage App Secrets"],
  ])("labels %s with its console's terms and says where to find them", (platform, name, idLabel, secretLabel, where) => {
    renderCard(platform, name);

    const id = screen.getByLabelText(idLabel);
    const secret = screen.getByLabelText(secretLabel);
    expect(hintOf(id) + " " + hintOf(secret)).toContain(where);
  });

  it("falls back to the generic Client ID and Client secret for a platform with no entry", () => {
    // Trovo is deliberately absent from the table (it is being retired), so
    // it is the real-world case of this fallback.
    renderCard("trovo", "Trovo");

    const id = screen.getByLabelText("Client ID");
    screen.getByLabelText("Client secret");
    expect(id.getAttribute("aria-describedby")).toBeNull();
  });
});
