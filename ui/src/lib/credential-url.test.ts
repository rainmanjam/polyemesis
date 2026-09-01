import { describe, expect, it } from "vitest";
import { urlCarriesCredential } from "./credential-url";

/* THIS FUNCTION DECIDES WHETHER A STRING GOES ON SCREEN.
 *
 * Wrong in one direction it masks a plain RTMP address, which trains an
 * operator to press reveal on everything until the mask means nothing. Wrong in
 * the other it prints an SRT passphrase in a <code> block on the dashboard,
 * which is the bug the whole change exists to fix. Neither failure announces
 * itself: both render as a page that looks fine.
 *
 * So the cases below are the two shapes internal/api/handlers.go actually
 * constructs, plus the near-misses that would fool a looser check. */

describe("urlCarriesCredential", () => {
  it("is true for an SRT ingest URL carrying a passphrase", () => {
    expect(
      urlCarriesCredential("srt://host.example:6000?latency=200&passphrase=hunter2"),
    ).toBe(true);
  });

  it("is true when passphrase is the first query parameter", () => {
    // `?passphrase=` and `&passphrase=` are different bytes; a check written
    // for one silently misses the other.
    expect(urlCarriesCredential("srt://host.example:6000?passphrase=hunter2")).toBe(true);
  });

  it("is true for a pull URL with userinfo", () => {
    expect(urlCarriesCredential("rtsp://user:pass@camera.local/stream")).toBe(true);
  });

  it("is true for userinfo with no password", () => {
    expect(urlCarriesCredential("rtsp://user@camera.local/stream")).toBe(true);
  });

  it("is false for a plain RTMP address", () => {
    // The overwhelmingly common case, and the one that must stay readable: an
    // operator checks the host at a glance.
    expect(urlCarriesCredential("rtmp://host.example:1935/live")).toBe(false);
  });

  it("is false for SRT with no passphrase", () => {
    expect(urlCarriesCredential("srt://host.example:6000?latency=200")).toBe(false);
  });

  it("does not count an @ outside the authority", () => {
    // The near-miss that a bare `includes("@")` gets wrong. An '@' in a path or
    // query is not userinfo, and masking on it would hide an address that is
    // not secret.
    expect(urlCarriesCredential("rtmp://host.example:1935/live/user@example.com")).toBe(false);
    expect(urlCarriesCredential("rtmp://host.example:1935/live?label=a@b")).toBe(false);
  });

  it("does not match a parameter that merely ends in passphrase", () => {
    // `?haspassphrase=false` says a passphrase is absent. Matching it would
    // mask a URL precisely when it is safe.
    expect(urlCarriesCredential("srt://host.example:6000?haspassphrase=false")).toBe(false);
  });

  it("is false for an empty or absent URL", () => {
    // The unprobed source and the pre-mode source both reach the page as "".
    // A warning-shaped mask on an empty string is noise on every fresh install.
    expect(urlCarriesCredential("")).toBe(false);
  });

  it("ignores case in the parameter name", () => {
    expect(urlCarriesCredential("srt://host.example:6000?Passphrase=x")).toBe(true);
  });
});
