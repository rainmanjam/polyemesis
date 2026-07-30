# TLS and certificates

polyemesis can terminate TLS itself. `tls.mode` chooses how, and
`config.example.yaml` ships `auto`, which decides at startup.

If you read one section of this page, make it
[Binding, and the SSH tunnel](#binding-and-the-ssh-tunnel) — plain HTTP on every
interface is the single biggest practical exposure this product has.

- [Modes](#modes)
- [How `auto` decides](#how-auto-decides)
- [Upgrading from `tls.enabled`](#upgrading-from-tlsenabled)
- [Worked configurations](#worked-configurations)
- [Trusting the self-signed CA](#trusting-the-self-signed-ca)
- [ACME needs port 80](#acme-needs-port-80)
- [HSTS is opt-in, and here is why](#hsts-is-opt-in-and-here-is-why)
- [Binding, and the SSH tunnel](#binding-and-the-ssh-tunnel)
- [Behind a reverse proxy](#behind-a-reverse-proxy)
- [The plain-HTTP companion on :80](#the-plain-http-companion-on-80)
- [Security headers](#security-headers)
- [Transport security beyond the UI](#transport-security-beyond-the-ui)

---

## Modes

| mode | certificate from | browser warning | needs |
|---|---|---|---|
| `auto` | resolves to one of the four below | depends | nothing |
| `acme` | Let's Encrypt, issued on demand | none | public DNS name, `acmeEmail`, inbound port 80 |
| `selfsigned` | a CA generated on this box | yes, until you trust that CA | nothing |
| `manual` | `certFile` / `keyFile` you supply | none, if their issuer is trusted | those two files |
| `off` | nothing — plain HTTP | n/a | something else terminating TLS |

Whenever polyemesis is terminating TLS, the listener pins **TLS 1.2 as the
floor** and prefers X25519, then P-256 and P-384. Go's server default already
floors at 1.2; pinning it means a future toolchain default cannot quietly change
what this server accepts.

## How `auto` decides

At startup, in this order:

1. `trustProxyHeaders: true` → **off**. You have told polyemesis a reverse proxy
   sits in front of it, so the proxy owns TLS.
2. `hostname` is a public FQDN **and** `acmeEmail` is set → **acme**.
3. anything else → **selfsigned**.

A *public FQDN* contains a dot, is not an IP literal, and does not end in
`.local`, `.internal`, `.lan`, `.home`, `.arpa` or `localhost` — a name Let's
Encrypt could plausibly validate. `stream.example.com` qualifies;
`polyemesis.lan`, `nas.local` and `192.168.1.10` do not.

Rule 2 needs *both* a public name and a contact address, so a box with real DNS
but no `acmeEmail` falls to self-signed rather than repeatedly failing issuance.

If `hostname` is empty and the resolved mode is `selfsigned`, the system
hostname is used, so the certificate always has a name in it.

## Upgrading from `tls.enabled`

`tls.enabled` is still parsed, and is consulted **only when `tls.mode` is
absent**:

| existing config | behaves as |
|---|---|
| `enabled: true` with `certFile`/`keyFile` | `mode: manual` — your certificate keeps being served |
| `enabled: false`, or no `tls:` block at all | `mode: off` — plain HTTP, exactly as before |

An explicit `tls.mode` always wins, so you can migrate without deleting the old
key, and an upgrade never swaps a real certificate for a self-signed one or
silently stops serving HTTPS.

Note that this also means **an existing install does not get `auto` for free** —
it keeps doing what it did yesterday until you write `mode: auto` yourself.

## Worked configurations

### 1. Public server with a DNS name — the recommended deployment

```yaml
addr: ":443"
tls:
  mode: "auto"                    # resolves to acme
  hostname: "stream.example.com"
  acmeEmail: "ops@example.com"
  hsts: true                      # safe here: publicly trusted certificate
```

Point an A/AAAA record at the box and open **80 and 443**. The certificate is
issued lazily, on the first HTTPS handshake for that name, and renewed
automatically.

Tradeoff: you depend on Let's Encrypt being reachable and on your DNS being
correct, and issuance is pinned to that one hostname — a request arriving with
any other SNI is refused rather than triggering a new order, which is what stops
a public port being used to burn your rate limit.

### 2. Homelab box with no public DNS

```yaml
tls:
  mode: "auto"                    # resolves to selfsigned
  hostname: "polyemesis.lan"
```

polyemesis mints a local CA and a leaf for that name, plus `localhost`,
`127.0.0.1` and `::1` so the first login over an SSH tunnel or by loopback does
not warn either.

Tradeoff: every browser warns until you
[install the CA](#trusting-the-self-signed-ca), and mobile clients are genuinely
annoying to convince. In exchange the traffic is encrypted, which is the part
that matters on a shared LAN.

If you reach the box by LAN address rather than by name, put the address in
`hostname` — an IP literal is accepted and becomes a SAN. A certificate naming
only `polyemesis.lan` will still warn when you browse to `https://192.168.1.10`,
because the name you typed is not in it. Changing `hostname` reissues the leaf
on the next start; the CA, and everything that already trusts it, is untouched.

### 3. Behind nginx / Caddy / Traefik

```yaml
addr: "127.0.0.1:8080"
trustProxyHeaders: true
tls:
  mode: "auto"                    # resolves to off
```

The proxy owns TLS, HSTS and the redirect. See
[Behind a reverse proxy](#behind-a-reverse-proxy).

### 4. Your own certificate

Corporate CA, wildcard, cert-manager, an existing certbot:

```yaml
tls:
  mode: "manual"
  hostname: "stream.example.com"  # used for the HTTP→HTTPS redirect target
  certFile: "/etc/ssl/polyemesis/fullchain.pem"
  keyFile:  "/etc/ssl/polyemesis/privkey.pem"
  hsts: true                      # only if the issuer is publicly trusted
```

`certFile` should be the full chain, leaf first. Both files are read at startup
and never rewritten.

Tradeoff: **renewal is yours.** polyemesis loads the pair once, so a certbot
renewal needs a `systemctl restart polyemesis` (or a `--deploy-hook`) before the
new certificate is served. Missing or mismatched files are a hard startup error,
naming both paths.

### 5. Plain HTTP on purpose

```yaml
addr: "127.0.0.1:8080"
tls:
  mode: "off"
```

Fine on loopback. On any other address it is the worst thing in this document —
see [Binding, and the SSH tunnel](#binding-and-the-ssh-tunnel).

## Trusting the self-signed CA

The generated material lives in `<dataDir>/tls/` (directory `0700`, private keys
`0600`, never logged and never returned by any API):

```
<dataDir>/tls/ca.crt        the local CA — this is the file you install
<dataDir>/tls/ca.key        its private key. Never leaves the box.
<dataDir>/tls/server.crt    the leaf, followed by the CA, as a chain
<dataDir>/tls/server.key    the leaf's private key
```

The CA is valid for ten years; the leaf for one, and it is regenerated
automatically within 30 days of expiry or if you change `tls.hostname`. That
split is on purpose: installing a CA into a browser, a phone and a keychain is
the most tedious step of a homelab setup, and making you redo it annually would
be a reason to give up on HTTPS entirely.

Copy the CA to the machine you browse from and **check the fingerprint** against
the `ca sha-256` line polyemesis prints at startup before you trust it:

```bash
scp user@host:/var/lib/polyemesis/tls/ca.crt ./polyemesis-ca.crt
openssl x509 -in polyemesis-ca.crt -noout -fingerprint -sha256
```

The server also offers it at `GET /api/v1/tls/ca`, which needs no session. On a
fresh box the browser will not let you reach the login form until the CA is
installed, so gating the download behind a sign-in would deadlock the only way
out of that. It is the public half of the CA, which every client already
receives during the handshake; the private key has no route. The Settings page
links to it, or:

```bash
curl -k https://polyemesis.lan:8443/api/v1/tls/ca -o polyemesis-ca.crt
```

Check the fingerprint either way — `-k` means you have not yet verified who
answered.

**macOS** (Keychain Access will also do this by drag-and-drop):

```bash
sudo security add-trusted-cert -d -r trustRoot \
  -k /Library/Keychains/System.keychain polyemesis-ca.crt
```

**Linux**, Debian/Ubuntu:

```bash
sudo cp polyemesis-ca.crt /usr/local/share/ca-certificates/polyemesis.crt
sudo update-ca-certificates
```

Fedora/RHEL: drop it in `/etc/pki/ca-trust/source/anchors/` and run
`sudo update-ca-trust`.

> Firefox — and Chrome on Linux — do **not** use the system store. Firefox:
> *Settings → Privacy & Security → Certificates → View Certificates →
> Authorities → Import*, and tick "identify websites". Chrome on Linux reads
> NSS: `certutil -d sql:$HOME/.pki/nssdb -A -t "C,," -n polyemesis -i polyemesis-ca.crt`.

**Windows**, PowerShell as Administrator:

```powershell
Import-Certificate -FilePath .\polyemesis-ca.crt `
  -CertStoreLocation Cert:\LocalMachine\Root
```

**iOS/Android** both need two steps — install the profile *and* then explicitly
enable it as a trusted root (iOS: *Settings → General → About → Certificate
Trust Settings*). If that is more than you want to do, use mode `acme`, or reach
the UI over the [SSH tunnel](#binding-and-the-ssh-tunnel).

## ACME needs port 80

Let's Encrypt validates over **HTTP-01**, which means it must reach
`http://<hostname>/.well-known/acme-challenge/…` on **port 80** from the public
internet. Open it on the firewall and in any NAT/port-forward. A CNAME to a CDN,
a captive portal, or an ISP that blocks 80 will all break issuance.

polyemesis also advertises the **TLS-ALPN-01** protocol on its HTTPS listener,
so on a box where 443 is reachable but 80 is not, issuance can still succeed
that way. Treat that as a fallback, not a plan.

If port 80 cannot be bound — already taken, or the process is unprivileged —
polyemesis **logs a warning and carries on serving**. It does not refuse to
start. That is deliberate: a server that dies over a certificate problem leaves
you with no UI in which to fix the setting that killed it. The log carries:

```text
cannot bind :80 for the acme http-01 challenge; certificate issuance will keep
failing until port 80 reaches this host (free the port, grant
CAP_NET_BIND_SERVICE, or forward it). Serving https meanwhile
```

and the startup banner says the certificate has not been issued yet.

Under systemd, the usual fix is `AmbientCapabilities=CAP_NET_BIND_SERVICE` — see
the commented block in
[`deploy/polyemesis.service`](../deploy/polyemesis.service).

**Back up `<dataDir>/tls/acme/`.** It holds your ACME account key and every
issued certificate; a redeploy that loses it re-orders from scratch, and Let's
Encrypt's duplicate-certificate limit is not generous.

In Docker, **port 80 is commented out in `docker-compose.yml`** and needs
uncommenting for exactly this. It ships off because publishing `:80`
unconditionally breaks `docker compose up` on any host already running a web
server.

## HSTS is opt-in, and here is why

`tls.hsts` defaults to `false`. Turn it on only when you have a certificate a
browser will validate without help.

`Strict-Transport-Security` tells a browser "never speak plain HTTP to this
hostname again, and never let the user click through a certificate warning for
it". The browser remembers that **for the whole max-age, on the client**, and
there is no way for the server to take it back — clearing it means clearing site
data in every browser on every device that saw the header.

Now picture that on a homelab box with a self-signed certificate. The browser
has been told to refuse plain HTTP to `polyemesis.lan`, and HSTS also removes
the "Advanced → Proceed anyway" escape hatch for the untrusted certificate. Both
doors are shut, from one header, and rebuilding the server does not reopen them.

So:

- **Never sent unless the connection really is HTTPS.** The check is Go's
  `r.TLS`, not a forwarded header — a header can be forged, and behind a trusted
  proxy the policy for the connection the browser actually made belongs to
  whoever terminated it.
- **Never sent in `selfsigned` mode, even with `hsts: true`.** polyemesis logs a
  warning at startup explaining that it is being suppressed, rather than quietly
  obeying you into a lockout.
- **Never sent when the resolved mode is `off`** — same warning.
- When it *is* sent it is `max-age=86400`, one day. **No `includeSubDomains`, no
  `preload`.** Both widen the blast radius past this one host, and preload in
  particular is close to irreversible. A day is long enough to be a real
  downgrade defence and short enough that a mistake ages out.

If you want a long max-age and preloading, set it on the reverse proxy, where
you already own the whole origin and can undo it.

## Binding, and the SSH tunnel

The default `addr` is `":8080"` — **every interface**. Plain HTTP on every
interface is the single biggest practical exposure this product has: the login
form and the session cookie cross the network in clear text, and anyone on the
path can read or replay them. polyemesis prints a loud warning at startup when
it detects exactly that combination (binds publicly, not terminating TLS, no
trusted proxy).

Fix it by enabling TLS — or, if you just want it private with no certificates at
all, bind to loopback and tunnel:

```yaml
addr: "127.0.0.1:8080"
tls:
  mode: "off"
```

```bash
ssh -N -L 8080:127.0.0.1:8080 user@host
```

Then open <http://localhost:8080>. SSH carries the encryption and the
authentication, nothing is exposed on the box, and there is no certificate to
install anywhere. For a single admin this is the lowest-effort secure setup
there is.

Two caveats, both real:

- **The tunnel only covers the web UI.** The ingest listener binds `0.0.0.0` on
  its own port regardless of `addr`, because your encoder has to reach it. Guard
  that with the SRT passphrase and a firewall rule — see
  [Transport security beyond the UI](#transport-security-beyond-the-ui).
- The HLS preview and the WebSocket both travel inside the tunnel and work
  normally; nothing in the UI needs a second port.

## Behind a reverse proxy

The other common deployment terminates TLS at nginx (or Caddy, or Traefik) and
lets polyemesis listen on plain HTTP behind it. A complete example is in
[`deploy/nginx.conf.example`](../deploy/nginx.conf.example).

With `trustProxyHeaders: true`, `mode: auto` resolves to **off** — polyemesis
does not try to obtain or serve a certificate, does not bind port 80, and sends
no HSTS. The proxy owns all three. That is the intended interaction, not a
limitation: two things fighting over port 80 for ACME is a much worse day than
one.

Four things matter:

1. **Set `trustProxyHeaders: true`.** polyemesis then honours
   `X-Forwarded-Proto` and `X-Forwarded-Host` when marking session cookies
   `Secure` and when building OAuth redirect URIs. Leave it `false` when there is
   no proxy — otherwise a client can forge those headers.
2. **Bind polyemesis to loopback** (`addr: "127.0.0.1:8080"`). With a proxy in
   front there is no reason for the plaintext port to be reachable from anywhere
   else, and `trustProxyHeaders` suppresses the exposure warning that would
   otherwise have told you about it.
3. **Proxy the WebSocket.** Live status, meters and logs all arrive over
   `/api/v1/ws`: `proxy_http_version 1.1`, `Upgrade`/`Connection "upgrade"`, and
   a long `proxy_read_timeout`.
4. **Do not proxy the ingest.** SRT is UDP and RTMP is not HTTP; neither travels
   through an HTTP reverse proxy. Open those ports directly on the firewall.

Also turn buffering off (`proxy_buffering off`) or the HLS preview will lag, and
set `client_max_body_size 0` so multi-gigabyte recording downloads work.

If you want HSTS in this deployment, set it on the proxy. polyemesis will not
send it with `mode: off` however `tls.hsts` is set, and says so at startup.

## The plain-HTTP companion on :80

Whenever polyemesis is terminating TLS itself, it also tries to bind `:80` for a
small helper that does two jobs: answers ACME HTTP-01 challenges (acme mode
only) and permanently redirects everything else to HTTPS. Redirects are `301`
for `GET`/`HEAD` and `308` for everything else, so an API client's method and
body survive the hop.

It is skipped when `addr` is already port 80, and a failure to bind is a warning
rather than a fatal error — you keep your HTTPS listener and your UI either way.

## Security headers

Every response carries:

| header | value |
|---|---|
| `Content-Security-Policy` | `default-src 'self'` plus the relaxations below |
| `X-Frame-Options` | `DENY` |
| `X-Content-Type-Options` | `nosniff` |
| `Referrer-Policy` | `no-referrer` |
| `Permissions-Policy` | `camera=(), microphone=(), geolocation=()` |
| `Strict-Transport-Security` | only under the conditions above |

The CSP relaxations exist for specific features and each one is load-bearing:
`media-src 'self' blob:` and `worker-src 'self' blob:` because hls.js hands
`<video>` a blob URL and compiles its demuxer worker from generated source;
`connect-src 'self' ws: wss:` for the telemetry WebSocket, with `ws:` because a
LAN box may legitimately be on plain HTTP; `img-src 'self' data:` for inline
icons; `style-src 'self' 'unsafe-inline'` because the bundle injects `<style>` at
runtime.

Notably **absent** is `'unsafe-inline'` for scripts — the UI is a Vite bundle of
hashed module files with no inline `<script>`, and that is the one relaxation
that would turn an injected string into executable code.

## Transport security beyond the UI

TLS on the web UI is not the whole story:

- **SRT ingest has its own encryption.** Set a passphrase under *Settings →
  Ingest* (SRT requires 10–79 characters, which polyemesis enforces in the form)
  and SRT encrypts the stream with AES. The dashboard renders the exact
  `srt://…?passphrase=…` URL to paste into OBS. Without one, your stream —
  including anything on screen — crosses the network unencrypted.
- **RTMP ingest has no equivalent.** RTMP is authenticated by the stream key in
  the URL and is otherwise in the clear. It is the fallback for encoders that
  cannot do SRT; prefer SRT where you have the choice.
- **Destinations can be `rtmps://`.** RTMP destination URLs accept both
  `rtmp://` and `rtmps://` and the URL is handed to FFmpeg verbatim, so where a
  platform publishes an RTMPS ingest address, paste that one. SRT destinations
  are passed through unchanged too — append your own `?passphrase=…` if the
  receiving end expects one.

---

## See also

- [CONFIGURATION.md](CONFIGURATION.md) — the whole `config.yaml` surface
- [ARCHITECTURE.md](ARCHITECTURE.md#4-the-http-edge-tls-and-security-headers) —
  where the TLS decision lives in the code
- [../SECURITY.md](../SECURITY.md) — threat model, and what is deliberately not
  defended
- [TROUBLESHOOTING.md](TROUBLESHOOTING.md#it-will-not-start) — startup warnings
  about TLS
