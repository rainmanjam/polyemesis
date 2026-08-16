import { describe, expect, it } from "vitest";

import {
  acmeStance,
  acmeYaml,
  firstBlocker,
  offersPreflight,
  suggestedHostname,
} from "./acme-guidance";
import type { AcmeCheck, AcmePreflight, TlsStatus } from "./types";

/* The walkthrough's judgement, tested where it lives.
 *
 * Every one of these is about telling an operator the truth in a state they did
 * not choose. The expensive mistakes are symmetric: telling someone behind a
 * working reverse proxy that their TLS needs fixing, and telling someone on a
 * self-signed certificate with a real domain that there is nothing to do. */

function tls(over: Partial<TlsStatus> = {}): TlsStatus {
  return {
    mode: "selfsigned",
    configured: "auto",
    hostname: "",
    servesTls: true,
    trustProxyHeaders: false,
    hsts: false,
    hstsWarning: "",
    certificate: null,
    certificateError: "",
    caAvailable: true,
    caFingerprint: "AA:BB",
    ...over,
  };
}

const cert: TlsStatus["certificate"] = {
  subject: "CN=stream.example.com",
  issuer: "CN=R3",
  dnsNames: ["stream.example.com"],
  ipAddresses: [],
  notBefore: "2026-01-01T00:00:00Z",
  notAfter: "2026-04-01T00:00:00Z",
  daysRemaining: 60,
  expired: false,
  fingerprint: "AA:BB",
  selfSigned: false,
};

describe("which situation the operator is in", () => {
  it("calls a self-signed server switchable, which is the whole point", () => {
    expect(acmeStance(tls({ mode: "selfsigned" }))).toBe("switchable");
  });

  it("calls plain HTTP with nothing in front switchable too", () => {
    // The operator who most needs this reaches the UI over HTTP, so the
    // walkthrough has to be there as well — and that is the case where the
    // password crossing the wire is itself the thing being fixed.
    expect(acmeStance(tls({ mode: "off", servesTls: false }))).toBe("switchable");
  });

  it("does not tell a working reverse-proxy deployment to fix anything", () => {
    // TLS is terminated correctly one hop away. A panel urging a certificate
    // here is advice to break a setup that is right.
    expect(acmeStance(tls({ mode: "off", servesTls: false, trustProxyHeaders: true }))).toBe(
      "proxy",
    );
    expect(offersPreflight("proxy")).toBe(false);
  });

  it("separates ACME that worked from ACME that has not", () => {
    expect(acmeStance(tls({ mode: "acme", certificate: cert }))).toBe("trusted");
    // No certificate in acme mode is not a hypothetical to plan for: it is a
    // failure happening now, and the same checks explain it.
    expect(acmeStance(tls({ mode: "acme", certificate: null }))).toBe("issuing");
    expect(offersPreflight("issuing")).toBe(true);
    expect(offersPreflight("trusted")).toBe(false);
  });

  it("leaves an operator with their own certificate alone, but still lets them ask", () => {
    expect(acmeStance(tls({ mode: "manual", certificate: cert }))).toBe("own-cert");
    expect(offersPreflight("own-cert")).toBe(true);
  });
});

describe("which hostname to check", () => {
  it("prefers what the server is configured as", () => {
    expect(suggestedHostname(tls({ hostname: "stream.example.com" }), "10.0.0.4")).toBe(
      "stream.example.com",
    );
  });

  it("falls back to the name in the address bar, which is what off mode has", () => {
    expect(suggestedHostname(tls({ mode: "off", hostname: "" }), "stream.example.com")).toBe(
      "stream.example.com",
    );
  });

  it("prefills nothing that could never receive a certificate", () => {
    // Prefilling one of these produces a check that fails on arrival, which
    // reads as "this server cannot have a certificate" rather than as "type
    // the name you actually use".
    for (const host of ["192.168.1.10", "polyemesis", "nas.local", "box.lan", "[::1]"]) {
      expect(suggestedHostname(tls({ hostname: "" }), host), host).toBe("");
    }
  });
});

describe("the config to write", () => {
  const yaml = acmeYaml("stream.example.com", "ops@example.com");

  it("carries the values the operator typed, under tls:", () => {
    expect(yaml).toContain("tls:");
    expect(yaml).toContain("  hostname: stream.example.com");
    expect(yaml).toContain("  acmeEmail: ops@example.com");
    expect(yaml).toContain("  mode: acme");
  });

  it("leaves HSTS off", () => {
    // The one line in this snippet with no undo. Pinning a browser to a host
    // whose first issuance then fails locks the operator out of their own UI,
    // and nothing on the server can lift it.
    expect(yaml).not.toMatch(/^\s*hsts: true/m);
    expect(yaml).toContain("# hsts: true");
  });

  it("still reads as valid YAML with nothing filled in", () => {
    const blank = acmeYaml("", "");
    expect(blank).toContain("  hostname: stream.example.com");
    expect(blank).toContain("  acmeEmail: you@example.com");
  });
});

describe("what to read first", () => {
  const check = (id: AcmeCheck["id"], status: AcmeCheck["status"]): AcmeCheck => ({
    id,
    status,
    detail: `${id} ${status}`,
  });
  const preflight = (checks: AcmeCheck[]): AcmePreflight => ({
    hostname: "stream.example.com",
    mode: "selfsigned",
    ready: !checks.some((c) => c.status === "fail"),
    checks,
  });

  it("names a failure ahead of an unanswerable question", () => {
    // Port 80's reachability from outside can never be settled from in here.
    // Leading with it, while the name is one nothing can issue for, sends the
    // operator to their firewall over a problem in their config file.
    const got = firstBlocker(preflight([check("port80", "unknown"), check("name", "fail")]));
    expect(got?.id).toBe("name");
  });

  it("orders failures the way issuance runs into them", () => {
    const got = firstBlocker(preflight([check("email", "fail"), check("dns", "fail")]));
    expect(got?.id).toBe("dns");
  });

  it("has nothing to say when every check passed", () => {
    expect(firstBlocker(preflight([check("name", "pass"), check("dns", "pass")]))).toBeNull();
  });
});
