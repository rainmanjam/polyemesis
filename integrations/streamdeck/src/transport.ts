import http from "node:http";
import https from "node:https";
import tls from "node:tls";
import { TransportError, type Transport, type TransportRequest, type TransportResponse } from "./api.js";

const TIMEOUT_MS = 5000;

// Certificate failures that mean "this is a certificate we do not trust", as
// opposed to "nothing answered". Node reports them with these codes.
const UNTRUSTED = /CERT|SELF_SIGNED|UNABLE_TO_VERIFY|UNABLE_TO_GET_ISSUER/;

/**
 * A transport over node:https. The install's CA, when pasted, is ADDED to the
 * system roots rather than replacing them, so a publicly trusted certificate
 * keeps working on the same install. Verification is never switched off.
 */
export function nodeTransport(caPem?: string): Transport {
  const ca = caPem && caPem.trim() !== "" ? [...tls.rootCertificates, caPem] : undefined;
  return {
    send(req: TransportRequest): Promise<TransportResponse> {
      return new Promise((resolve, reject) => {
        const request = (req.url.protocol === "https:" ? https : http).request(
          req.url,
          { method: req.method, headers: req.headers, ca, timeout: TIMEOUT_MS, rejectUnauthorized: true },
          (res) => {
            const chunks: Buffer[] = [];
            res.on("data", (c: Buffer) => chunks.push(c));
            res.on("end", () =>
              resolve({ status: res.statusCode ?? 0, body: Buffer.concat(chunks).toString("utf8") }),
            );
            res.on("error", (err) => reject(classify(err)));
          },
        );
        request.on("timeout", () => request.destroy(new TransportError("unreachable", "the server did not answer in time")));
        request.on("error", (err) => reject(classify(err)));
        if (req.body !== undefined) request.write(req.body);
        request.end();
      });
    },
  };
}

/** Maps a Node socket or TLS error onto the two transport failures the client knows. */
export function classify(err: unknown): TransportError {
  if (err instanceof TransportError) return err;
  const code = typeof (err as { code?: unknown })?.code === "string" ? String((err as { code: string }).code) : "";
  const message = err instanceof Error ? err.message : String(err);
  if (UNTRUSTED.test(code)) {
    return new TransportError("untrusted", "the server's certificate is not trusted; paste the install's CA");
  }
  return new TransportError("unreachable", message);
}
