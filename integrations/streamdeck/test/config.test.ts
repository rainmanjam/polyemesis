import { describe, expect, it } from "vitest";
import { ConfigError, keySettings, parseServerUrl, positiveId, requireToken } from "../src/config.js";

describe("parseServerUrl", () => {
  it("accepts https for any host", () => {
    expect(parseServerUrl("https://studio.example").protocol).toBe("https:");
  });

  it("accepts plain http only on loopback", () => {
    expect(parseServerUrl("http://localhost:8080").hostname).toBe("localhost");
    expect(parseServerUrl("http://127.0.0.1:8080").hostname).toBe("127.0.0.1");
  });

  it("refuses plain http to a remote host, because the token is a bearer credential", () => {
    expect(() => parseServerUrl("http://studio.example")).toThrow(ConfigError);
    expect(() => parseServerUrl("http://192.168.1.20")).toThrow("streamdeck.err.urlScheme");
  });

  it("refuses an empty or unparseable address", () => {
    expect(() => parseServerUrl("   ")).toThrow("streamdeck.err.urlRequired");
    expect(() => parseServerUrl("not a url")).toThrow("streamdeck.err.urlInvalid");
    expect(() => parseServerUrl(undefined)).toThrow(ConfigError);
  });
});

describe("requireToken", () => {
  it("trims and returns the token", () => {
    expect(requireToken("  abc  ")).toBe("abc");
  });

  it("refuses an empty token", () => {
    expect(() => requireToken("   ")).toThrow(ConfigError);
  });
});

describe("positiveId", () => {
  it("accepts a positive integer, as a number or a numeric string", () => {
    expect(positiveId(7)).toBe(7);
    expect(positiveId("7")).toBe(7);
  });

  it.each([0, -1, 1.5, "abc", "1.5", null, undefined, Number.NaN])("drops %s rather than guessing", (raw) => {
    expect(positiveId(raw)).toBeUndefined();
  });

  it("reads both key settings and leaves the unset ones undefined", () => {
    expect(keySettings({ sourceId: "3" })).toEqual({ sourceId: 3, destinationId: undefined });
    expect(keySettings(undefined)).toEqual({ sourceId: undefined, destinationId: undefined });
  });
});
