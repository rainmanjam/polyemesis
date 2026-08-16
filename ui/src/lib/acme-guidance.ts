import type { AcmeCheck, AcmePreflight, TlsStatus } from "./types";

/* The Let's Encrypt walkthrough's decisions, kept out of the component.
 *
 * WHAT THE WALKTHROUGH IS FOR. TLS in this product configures itself — Let's
 * Encrypt when the box has a public name, a local CA when it does not, out of
 * the way when a proxy terminates it. What an operator cannot see is WHICH OF
 * THE THREE they are in and what to do about it. Someone on a self-signed
 * certificate with a real domain already pointed at the box is one config
 * change away from a trusted one and has no way to find that out.
 *
 * So the panel answers three questions in order, and the third is the one
 * guides usually leave out: what am I on now, what would a trusted certificate
 * need, and what happens if I try. */

/** Which of the walkthrough's five situations this server is in.
 *
 *  `switchable` is the one it exists for. `issuing` is its unhappy twin — the
 *  operator already asked for Let's Encrypt and it is not working — and it gets
 *  the same checks pointed at a failure that has already happened rather than
 *  one that has not happened yet. */
export type AcmeStance =
  /** acme, with a certificate. Nothing to do; it renews itself. */
  | "trusted"
  /** acme, with nothing issued. Something is wrong now, not hypothetically. */
  | "issuing"
  /** manual. A trusted certificate already, from files the operator manages. */
  | "own-cert"
  /** off, behind a proxy. The certificate is the proxy's business. */
  | "proxy"
  /** selfsigned, or plain HTTP with nothing in front. The reason for all this. */
  | "switchable";

export function acmeStance(tls: TlsStatus): AcmeStance {
  switch (tls.mode) {
    case "acme":
      return tls.certificate ? "trusted" : "issuing";
    case "manual":
      return "own-cert";
    case "selfsigned":
      return "switchable";
    default:
      // `off` splits on whether someone else is doing the job. Behind a trusted
      // proxy, TLS is terminated correctly one hop away and this panel would be
      // telling the operator to fix something that is not broken. Without one,
      // the login form is crossing the network in plaintext and this is the
      // most useful thing on the page.
      return tls.trustProxyHeaders ? "proxy" : "switchable";
  }
}

/** Whether the panel offers to run the checks at all.
 *
 *  Not offered where there is nothing to learn: a working ACME certificate, and
 *  a proxy that owns TLS. Offered in `own-cert` because "could I stop managing
 *  these files myself?" is a real question with a real answer. */
export function offersPreflight(stance: AcmeStance): boolean {
  return stance === "switchable" || stance === "issuing" || stance === "own-cert";
}

/** The name to check, in descending order of how much it is worth believing.
 *
 *  The configured hostname first, because that is what this server would ask
 *  Let's Encrypt for. Then the name in the address bar — an operator on
 *  `tls.mode: off` has no configured hostname, and the name they typed to reach
 *  this page is the best evidence anywhere of what they intend to use. An
 *  address or a bare label is not a candidate: it can never receive a public
 *  certificate, and prefilling it would invite a check that always fails. */
export function suggestedHostname(tls: TlsStatus, browserHost: string): string {
  if (tls.hostname) return tls.hostname;
  return looksIssuable(browserHost) ? browserHost : "";
}

/** The same rule as config.IsPublicFQDN on the server, and only ever used to
 *  decide what to PREFILL — the server's answer is the one that is displayed,
 *  so the two drifting apart cannot produce a wrong verdict, only a field the
 *  operator has to fill in. */
function looksIssuable(host: string): boolean {
  const h = host.trim().toLowerCase().replace(/\.$/, "");
  if (!h.includes(".")) return false;
  if (/^[0-9.]+$/.test(h) || h.includes(":")) return false;
  return ![".local", ".internal", ".lan", ".home", ".arpa", ".localhost"].some((s) =>
    h.endsWith(s),
  );
}

/** The config.yaml that switches this server to Let's Encrypt.
 *
 *  Written out in full rather than as a diff because it is going into a file
 *  the operator opens in an editor, and a fragment with no context is how
 *  `hostname` ends up at the top level instead of under `tls`.
 *
 *  HSTS IS COMMENTED OUT, which is a departure from docs/TLS.md's worked
 *  example and is deliberate. That example describes a deployment where
 *  issuance already works; this text is handed to someone whose first ACME
 *  restart has not happened yet. HSTS has no server-side undo, and telling a
 *  browser to refuse plain HTTP to a host whose certificate then fails to issue
 *  is the one mistake in this file that the operator cannot walk back. */
export function acmeYaml(hostname: string, email: string): string {
  return [
    "# config.yaml",
    '# Browsers assume 443. Drop this line if something in front of this box',
    "# already listens there.",
    'addr: ":443"',
    "tls:",
    "  mode: acme",
    `  hostname: ${hostname || "stream.example.com"}`,
    `  acmeEmail: ${email || "you@example.com"}`,
    "  # Turn this on only once the certificate is trusted; HSTS has no undo.",
    "  # hsts: true",
  ].join("\n");
}

/** The command that makes the file above take effect. TLS has to be settled
 *  before the listener opens, so there is no reload short of a restart. */
export const RESTART_COMMAND = "sudo systemctl restart polyemesis";

/** What the operator should read first: the check most likely to be the reason
 *  this will not work. A `fail` beats an `unknown` beats nothing.
 *
 *  Order matters here and follows the order issuance fails in: a name no CA can
 *  issue for is decided before DNS is consulted, DNS before a challenge is
 *  attempted, and everything before Let's Encrypt has an opinion. */
const CHECK_ORDER = ["name", "dns", "port80", "email", "issuance"] as const;

export function firstBlocker(p: AcmePreflight): AcmeCheck | null {
  for (const status of ["fail", "unknown"] as const) {
    for (const id of CHECK_ORDER) {
      const hit = p.checks.find((c) => c.id === id && c.status === status);
      if (hit) return hit;
    }
  }
  return null;
}
