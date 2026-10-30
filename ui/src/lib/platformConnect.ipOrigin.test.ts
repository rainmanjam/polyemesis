import { describe, expect, it } from "vitest";
import { isIpLiteralHost } from "./platformConnect";

/* A sign-in that redirects back needs an address a platform can register. A
 * bare IP address is one it usually cannot, so the dialog says so before the
 * operator clicks a button that will fail. Only the literal is detectable from
 * the browser: a self-signed certificate on a hostname is not, and this does
 * not pretend it is. */
describe("isIpLiteralHost", () => {
  it("is true for an IPv4 address", () => {
    expect(isIpLiteralHost("192.168.1.50")).toBe(true);
    expect(isIpLiteralHost("10.0.0.1")).toBe(true);
  });

  it("is true for a bracketed or bare IPv6 address", () => {
    expect(isIpLiteralHost("[fd00::1]")).toBe(true);
    expect(isIpLiteralHost("fd00::1")).toBe(true);
  });

  it("is false for a hostname, including one that merely contains digits", () => {
    expect(isIpLiteralHost("studio.example.com")).toBe(false);
    expect(isIpLiteralHost("host1.lan")).toBe(false);
    expect(isIpLiteralHost("localhost")).toBe(false);
  });

  it("is false for four dotted numbers that are not an address", () => {
    expect(isIpLiteralHost("999.1.1.1")).toBe(false);
  });

  it("is false for an empty host", () => {
    expect(isIpLiteralHost("")).toBe(false);
  });
});
