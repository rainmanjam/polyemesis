import { describe, expect, it } from "vitest";

import { SERVER_PLACEHOLDER, withServerHost } from "./publish-url";

/* Reported from the product: copying the SRT or RTMP ingest URL puts `<server>`
 * on the clipboard instead of the hostname. The server emits that placeholder on
 * purpose -- it does not know which of its addresses the operator can reach --
 * and nothing in the UI was filling it in, so the page displayed it verbatim and
 * the copy button handed it to OBS. */
describe("withServerHost", () => {
  it("fills the placeholder the server sends", () => {
    expect(
      withServerHost("rtmp://<server>:1935/live", "stream.example.com"),
    ).toBe("rtmp://stream.example.com:1935/live");
    expect(withServerHost("srt://<server>:6000?mode=caller", "10.0.0.4")).toBe(
      "srt://10.0.0.4:6000?mode=caller",
    );
  });

  it("replaces every occurrence, not just the first", () => {
    expect(withServerHost("a <server> b <server>", "h")).toBe("a h b h");
  });

  it("brackets an IPv6 literal, or the colons read as a port", () => {
    expect(withServerHost("rtmp://<server>:1935/live", "2001:db8::1")).toBe(
      "rtmp://[2001:db8::1]:1935/live",
    );
    // Already bracketed stays as it is rather than being double-wrapped.
    expect(withServerHost("rtmp://<server>:1935/live", "[::1]")).toBe(
      "rtmp://[::1]:1935/live",
    );
  });

  it("leaves the placeholder when there is no host to put there", () => {
    // `rtmp://:1935/live` is worse than a visible placeholder: one is obviously
    // unfinished, the other looks like an address and fails later.
    expect(withServerHost("rtmp://<server>:1935/live", "")).toBe(
      "rtmp://<server>:1935/live",
    );
    expect(withServerHost("rtmp://<server>:1935/live", "   ")).toContain(
      SERVER_PLACEHOLDER,
    );
  });

  it("leaves a URL that has no placeholder alone", () => {
    expect(withServerHost("rtmp://already.example/live", "h")).toBe(
      "rtmp://already.example/live",
    );
  });
});
