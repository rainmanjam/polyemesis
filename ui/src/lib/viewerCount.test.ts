import { describe, expect, it } from "vitest";

import { VIEWER_POLL_MS, viewerReadout } from "./viewerCount";
import type { AccountStats } from "./types";

import de from "./i18n/de.json";
import en from "./i18n/en.json";
import es from "./i18n/es.json";
import fr from "./i18n/fr.json";
// Aliased, both of them: `it` is vitest's, and `id` reads as an identifier
// everywhere else in this file.
import idID from "./i18n/id.json";
import itIT from "./i18n/it.json";
import ja from "./i18n/ja.json";
import ko from "./i18n/ko.json";
import nl from "./i18n/nl.json";
import pl from "./i18n/pl.json";
import ptBR from "./i18n/pt-BR.json";
import ru from "./i18n/ru.json";
import tr from "./i18n/tr.json";
import uk from "./i18n/uk.json";
import zhHans from "./i18n/zh-Hans.json";

/* The distinction this whole feature is about: a platform that DECLINED to say
 * how many people are watching must not be reported as an audience of nobody.
 *
 * internal/oauth/stats.go makes ViewerCount a *int with omitempty for exactly
 * this, and names the three ways YouTube produces an absent key — nobody
 * watching, the OWNER HAS HIDDEN THE COUNT, the broadcast ended. The second is
 * the one that makes a false zero a bug report rather than a rounding error:
 * a streamer with an audience, told nobody is there. */

describe("viewerReadout", () => {
  it("does not report an absent count as zero", () => {
    // The shape internal/api actually sends: live, and no viewerCount key at
    // all. `stats.viewerCount` is `undefined` here, and `undefined ?? 0` is the
    // one-character mistake this asserts against.
    const res: AccountStats = {
      supported: true,
      stats: { live: true, source: "/streams" },
    };

    const readout = viewerReadout(res);

    expect(readout).toEqual({ kind: "notReported" });
    // Said twice on purpose: the kind above could be renamed, but a readout
    // that carries a zero anywhere is wrong whatever it is called.
    expect(JSON.stringify(readout)).not.toContain("0");
  });

  it("reports a real zero as a count, because a live stream nobody is watching is a fact", () => {
    const res: AccountStats = {
      supported: true,
      stats: { live: true, viewerCount: 0, source: "/streams" },
    };
    expect(viewerReadout(res)).toEqual({ kind: "count", count: 0 });
  });

  it("reports a count", () => {
    const res: AccountStats = {
      supported: true,
      stats: {
        live: true,
        viewerCount: 1312,
        title: "Friday night",
        category: "Just Chatting",
        startedAt: "2026-08-16T20:00:00Z",
        source: "/streams",
      },
    };
    expect(viewerReadout(res)).toEqual({ kind: "count", count: 1312 });
  });

  it("treats offline as an answer rather than an error", () => {
    const res: AccountStats = { supported: true, stats: { live: false, source: "/streams" } };
    expect(viewerReadout(res)).toEqual({ kind: "offline" });
  });

  it("does not turn an offline channel into a live one with a count", () => {
    // Twitch answers an empty data array when a channel is not live, which the
    // Go side reads as live:false. A stale count riding along on that payload
    // must not resurrect the stream.
    const res: AccountStats = {
      supported: true,
      stats: { live: false, viewerCount: 4200, source: "/streams" },
    };
    expect(viewerReadout(res)).toEqual({ kind: "offline" });
  });

  it("carries the server's reason through when the platform cannot be asked", () => {
    const res: AccountStats = {
      supported: false,
      reason: "polyemesis does not read a viewer count from facebook",
    };
    expect(viewerReadout(res)).toEqual({
      kind: "unsupported",
      reason: "polyemesis does not read a viewer count from facebook",
    });
  });

  it("does not accept a null or a string as a count", () => {
    // Neither shape is one the Go handler produces. Both are shapes a
    // hand-rolled client, a proxy or a future field rename could produce, and
    // `viewerCount != null` would have admitted the string while `?? 0` would
    // have turned the null into an audience of nobody.
    const nulled = {
      supported: true,
      stats: { live: true, viewerCount: null },
    } as unknown as AccountStats;
    expect(viewerReadout(nulled)).toEqual({ kind: "notReported" });

    const stringly = {
      supported: true,
      stats: { live: true, viewerCount: "0" },
    } as unknown as AccountStats;
    expect(viewerReadout(stringly)).toEqual({ kind: "notReported" });
  });

  it("does not fall over when supported is true and stats is missing", () => {
    const wrong = { supported: true } as unknown as AccountStats;
    expect(viewerReadout(wrong)).toEqual({ kind: "unreadable" });
  });
});

describe("VIEWER_POLL_MS", () => {
  it("is gentle enough not to take title push down with it", () => {
    // YouTube's Data API ceiling is 10,000 units a day for the whole PROJECT —
    // shared with metadata push, compliance and chat — and one Stats call is
    // three requests against it. A minute is ~120 units an hour per connected
    // account; anything under thirty seconds starts spending a broadcast's
    // title-push budget on a settings tab, and the refusal lands on whichever
    // feature asks next rather than on this one.
    //
    // This test exists because "make the viewer count more responsive" is a
    // reasonable-sounding change that breaks a different feature.
    expect(VIEWER_POLL_MS).toBeGreaterThanOrEqual(30_000);
  });
});

describe("the not-reported label, in every locale", () => {
  const CATALOGUES: Record<string, Record<string, string>> = {
    de,
    en,
    es,
    fr,
    id: idID,
    it: itIT,
    ja,
    ko,
    nl,
    pl,
    "pt-BR": ptBR,
    ru,
    tr,
    uk,
    "zh-Hans": zhHans,
  };

  it("covers all fifteen", () => {
    // internal/web/i18n_drift_test.go hardcodes the same 15 and errors on a key
    // missing from any of them. Asserted here too so a JS-side run catches it
    // without a Go toolchain.
    expect(Object.keys(CATALOGUES)).toHaveLength(15);
  });

  it("never contains a digit, in any language", () => {
    // A translation reading "0 gemeldet" or "视听人数 0" would reintroduce the
    // false zero through the catalogue, where no amount of correct TypeScript
    // would catch it. The English string carries no number, so neither should
    // any translation of it.
    for (const [locale, cat] of Object.entries(CATALOGUES)) {
      const s = cat["stats.notReported"];
      expect(s, `${locale} is missing stats.notReported`).toBeTruthy();
      expect(s, `${locale}: "${s}" contains a digit`).not.toMatch(/\d/);
      // An em dash, en dash or bare hyphen standing in for the number reads as
      // "nothing" at a glance, which is the same lie in punctuation.
      expect(s, `${locale}: "${s}" uses a dash as the number`).not.toMatch(/[—–]/);
    }
  });

  it("keeps the {count} placeholder in the count string", () => {
    for (const [locale, cat] of Object.entries(CATALOGUES)) {
      expect(cat["stats.watching"], `${locale} lost {count}`).toContain("{count}");
    }
  });
});
