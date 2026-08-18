# Configuration

The single most useful thing to understand about configuring polyemesis is
**where a setting lives**, because there are two places and they are not
interchangeable.

## The split

| | `config.yaml` (and flags) | The web UI (stored in SQLite) |
|---|---|---|
| **What** | Things that must be known *before the database opens* | Everything else |
| **Examples** | Listen address, data directory, TLS, FFmpeg paths | Ingest ports, destinations, routing, retention, credentials |
| **Changing it** | Edit the file, restart | Takes effect immediately |
| **Where it lives** | A file you manage | `<dataDir>/polyemesis.db` |

That is the whole rule. If a setting is needed to bring the server up, it is in
the file. If it describes what the server should *do*, it is in the UI.

This is why there is no `destinations:` block in `config.yaml`, and why people
looking for one do not find it: destinations are runtime state, edited while the
server is running, and a config file would be a second source of truth for them.

## config.yaml

Copy [`config.example.yaml`](../config.example.yaml) to `config.yaml` and edit.
**That file is the reference** — every key is documented inline, with worked
examples for the five common TLS deployments. It is deliberately the canonical
copy so there is no second version of the truth to drift.

The keys, in brief:

| Key | Default | What it is |
|---|---|---|
| `addr` | `":8080"` | HTTP listen address for the UI and API |
| `dataDir` | `"./data"` | Holds `polyemesis.db`, `secret.key`, `recordings/`, `hls/`, `tls/` |
| `tls.mode` | `"off"` — but `config.example.yaml` ships `"auto"` | `auto`, `acme`, `selfsigned`, `manual`, `off` |
| `tls.hostname` | `""` | The DNS name this server is reached by |
| `tls.acmeEmail` | `""` | Required for `acme`; where expiry warnings go |
| `tls.certFile` / `tls.keyFile` | `""` | `manual` mode only |
| `tls.hsts` | `false` | Opt-in on purpose — see below |
| `trustProxyHeaders` | `false` | Set **only** behind a reverse proxy you control |
| `ffmpeg.binary` / `ffmpeg.probe` | `""` | Pin specific binaries instead of searching `$PATH` |

### Flags

Every flag overrides the file. Useful for containers and for trying something
without editing anything.

```
-addr      HTTP listen address
-config    path to config.yaml           (default "config.yaml")
-data      data directory
-ffmpeg    path to the ffmpeg binary
-ffprobe   path to the ffprobe binary
-log       debug | info | warn | error   (default "info")
-version   print the version and exit

-reset-admin  set a new admin password and sign out every session, then exit
```

`-reset-admin` is for an operator who has shell access and no way in through the
UI. It asks twice without echoing, and it is safe to run against a live server:
it touches only the database and exits before anything binds a port. Piping the
password twice scripts it. See the FAQ for why deleting the row from the users
table is the wrong way to do this.

`-log debug` is the one to reach for when something is wrong. It logs each
child's full command line as it spawns, which is usually the fastest route to
understanding a process that will not start.

## Three things that surprise people

### The built-in default is `off`; `auto` comes from the example file

Those are two different things, and the difference is plain HTTP.

Start polyemesis with **no `tls:` block at all** — no config file, or a minimal
one — and it serves plaintext, because the compiled-in default is `off`. The
`auto` in the table above is what you get by starting from
`config.example.yaml`, which ships `mode: "auto"` uncommented. A new install
that follows the documented path is therefore encrypted; one assembled by hand
from the key list is not.

The server warns loudly at startup when it is binding publicly without TLS. That
warning is the thing to read.

### `tls.mode: auto` does not mean "always encrypt"

It means "pick the sensible mode for this deployment", and one of the outcomes
is `off`:

- `trustProxyHeaders: true` → **off** (your proxy is terminating TLS)
- a public hostname *and* `acmeEmail` set → **acme**
- anything else → **selfsigned**

A "public hostname" is one Let's Encrypt could actually validate: it has a dot,
is not an IP address, and does not end in `.local`, `.internal`, `.lan`,
`.home`, `.arpa` or `localhost`. If you expected ACME and got a self-signed
certificate, the hostname is almost always why.

### HSTS is opt-in, and that is deliberate

HSTS is remembered by the browser. Once it has seen the header for a hostname it
will refuse plain HTTP to that name until `max-age` elapses, and **the server
cannot take it back**. Turn it on when you have a publicly trusted certificate
and intend to keep one. It is ignored in `selfsigned` mode and when TLS is off —
both log a warning rather than failing, because pinning a certificate the browser
cannot validate is an excellent way to lock yourself out of your own tool.

### `enhancedRtmp` was removed

It is no longer a key, and it does not need reinstating. Enhanced RTMP /
multitrack FLV ingest (OBS 30.2+) turns out to work on **FFmpeg 7.1+** —
verified end to end — and not on FFmpeg 6.1.1, which is Ubuntu 24.04's stock
build. Either way there is nothing for a flag to switch: where it works, it
works by default, because the tracks arrive through the existing ingest command
unchanged. It confirms rather than undermines the removal — the key was declared
as a placeholder and kept on the belief that config files already carrying it
would otherwise fail to parse.

That belief was wrong: config loading ignores unrecognised keys, so the
declaration was buying nothing while presenting a knob an operator could set and
watch have no effect. **A config file that still names `enhancedRtmp` loads
exactly as before.** For multitrack ingest that is actually operated, use SRT:
Enhanced RTMP's version dependency is real, and OBS does not send multitrack
audio over it — measured against OBS 30.2.3, which emitted only legacy
single-track tags. See `evidence/enhanced-rtmp-multitrack.md`.

## Giving the box a real name

Most of what makes polyemesis awkward to set up — browser warnings, and every
platform refusing to accept a callback URL — is one problem wearing two hats:
the server has no name anyone else recognises. Three config keys fix both at
once, and the machinery is already here.

### What to do

1. **Point a DNS record at the box.**

   ```
   stream.example.com.   A   203.0.113.10
   ```

2. **Name it in `config.yaml`.**

   ```yaml
   tls:
     mode: auto                    # or acme, to be explicit
     hostname: stream.example.com
     acmeEmail: you@example.com
   ```

3. **Make port 443 reachable from the internet**, including *inbound* from
   Let's Encrypt. See the validation note below — this is the step people skip.

4. **Restart.** Nothing happens yet, on purpose.

### What actually happens

Startup does **no network I/O**. It resolves the mode (see
[`tls.mode: auto` does not mean "always encrypt"](#tlsmode-auto-does-not-mean-always-encrypt)),
then creates a certificate cache under the data directory, locked to this
account — it holds the ACME account key and every private key that is ever
issued.

Issuance happens on the **first HTTPS handshake for the configured name**. A
browser arrives, there is no certificate yet, and polyemesis orders one then.

Let's Encrypt validates by connecting **back to you**, and polyemesis answers
either of the two challenges it may choose:

- **HTTP-01, on port 80.** A small helper listener is started for this whenever
  TLS is enabled and the main listener does not already own the port. If it
  cannot bind, issuance keeps failing — the startup log calls this "the single
  most common reason Let's Encrypt issuance never completes".
- **TLS-ALPN-01, on port 443.** Answered on the listener already serving the UI,
  with no extra port.

**Open both 80 and 443 inbound.** Outbound-only firewalls and un-forwarded NAT
fail here, and the symptom is a browser certificate warning that never clears
rather than an obvious error. If port 80 is genuinely unavailable — a proxy owns
it, or the process cannot bind low ports — polyemesis says so rather than
leaving you to guess; see the certificate status in the UI.

**The first request after a cold start is slow**, because that is when issuance
runs. On a machine you are about to broadcast from, load the page once before
you need it.

Renewal is automatic and also happens on a handshake. The cache survives
restarts, so this is a first-boot cost, not a recurring one.

### If your DNS is on Cloudflare

Point the record at the box with the proxy **off** — the grey cloud, "DNS only":

```
stream.example.com    A    203.0.113.10    DNS only
```

Proxying it (the orange cloud) breaks certificate issuance in both directions at
once: Cloudflare terminates TLS, so the TLS-ALPN-01 challenge never reaches you,
and it proxies and caches HTTP, so the HTTP-01 challenge is unreliable at best.

There is a second reason, and for a streaming box it is the decisive one:
**Cloudflare's proxy carries HTTP and HTTPS only.** RTMP on 1935 and SRT over
UDP cannot pass through it at all, so a proxied name cannot serve your ingest
even when the certificate works.

If you specifically want the web UI behind the proxy, do not use ACME for it.
Issue a Cloudflare **Origin Certificate**, point `tls.mode: manual` at it with
`certFile` and `keyFile`, and set `trustProxyHeaders: true` so the callback URI
is built from the address the browser actually used. You will still need a
second, unproxied name for ingest.

### The part that pays for itself: platform sign-in

polyemesis builds every OAuth callback from the address **you reached it at**.
Browse to the real name and the callback becomes:

```
https://stream.example.com/api/v1/oauth/twitch/callback
```

That is a string a platform will accept. Paste it into each developer console —
Twitch, YouTube, Facebook, Kick all take it, with no relay, no device-code flow
and no second application to register.

This is the whole reason to bother. Connecting an account from a box reachable
only as `192.168.1.50`, or on a self-signed certificate, does not work at any
platform, because there is no address their console will let you register.

You do not have to assemble that string yourself. **Settings → the card for each
platform shows the exact URI to register**, built from the address you are
browsing right now, with a copy button beside it. It is computed rather than
described because getting it wrong — a stray slash, `http` where the platform
demands `https` — is the most common reason a setup fails, and the error the
platform returns names none of that.

The same card checks the URI before you paste it, and will tell you when:

- the address is plain HTTP on a routable host, which Google refuses outright
  and Twitch allows only for loopback;
- the address is a bare IP, which Google will not accept for a web application
  client;
- you are browsing one name while `tls.hostname` says another, which produces
  `redirect_uri_mismatch` at the platform;
- a reverse proxy is in front but `trustProxyHeaders` is off, so the URI on
  screen is not the one your browser actually used.

Loopback is deliberately exempt from the bare-IP warning: `http://127.0.0.1` is
what the platforms' own documentation recommends for local development, and
warning about the configuration they recommend is how operators learn to click
past warnings.

### Four things that catch people

**Issuance is rate limited, so a wrong DNS record is expensive.** polyemesis
pins issuance to the single configured name for exactly this reason: anyone who
can reach the port could otherwise make it request certificates for names you
do not own and exhaust the budget. Check the record resolves *before* the first
request, not after five failures.

**`trustProxyHeaders: true` turns this off.** It resolves to `off`, because it
means a reverse proxy is terminating TLS and issuing certificates is the
proxy's job. That is a supported setup; the callback URL then comes from the
`X-Forwarded-Host` and `X-Forwarded-Proto` headers your proxy sets, so it still
needs to be the public name.

**A hostname without a dot, an IP literal, or a `.local`/`.internal`/`.lan`
suffix is not a public name** and silently gives you a self-signed certificate
instead. If you expected ACME and did not get it, that is almost always why.

**Renaming later is not free.** The hostname ends up inside every redirect URI
you have registered at every platform. Changing it means re-registering all of
them, and existing connected accounts keep working only until their tokens need
a refresh through the old callback. Pick the name you intend to keep.

## Connecting accounts without a public address

[Giving the box a real name](#giving-the-box-a-real-name) is the durable answer.
This is the one for a box that does not have a name yet — a machine on your LAN,
a rented server you are still evaluating, or anything you reach over SSH.

It needs no certificate, no DNS record and no changes to polyemesis. It works
because every platform makes an exception for **loopback** addresses, and
because polyemesis builds its callback from the address *you* reached it at.

### Set it up

Bind to loopback only, and let SSH carry the encryption:

```yaml
addr: "127.0.0.1:8080"
tls:
  mode: "off"
```

Then, from the machine you browse on:

```
ssh -L 8080:127.0.0.1:8080 you@your-server
```

and open <http://localhost:8080>.

Turning TLS off is safe **only** in this shape. The listener is not reachable
from the network at all — SSH is the encrypted channel, and it is doing the same
job the "put a reverse proxy in front" advice describes. Do not combine
`tls.mode: off` with a public bind address; polyemesis warns loudly at startup
if you do, because that puts passwords and session cookies on the wire in the
clear.

### Register the callback

Settings shows the exact URI for each platform, with a copy button. Browsing at
`localhost:8080`, it will read:

```
http://localhost:8080/api/v1/oauth/twitch/callback
```

**Twitch** — paste it as an OAuth Redirect URL. Twitch permits plain HTTP for
loopback and nothing else, so this is the one case where `http://` is correct.

**YouTube** — create the OAuth client with application type **Desktop app**, not
Web application, and register the same URL with `/youtube/`. Google requires the
redirect to match exactly *including the port*, which is easy here: polyemesis
serves a fixed port from `addr`, so unlike a desktop application there is no
ephemeral port to reconcile.

Google will show an "unverified app" screen until the app is verified. For a
personal install, keeping the app in testing mode and adding yourself as a test
user is enough.

### Why not the device-code flow

For YouTube it is worth being explicit: Google's device flow is restricted to
`youtube` and `youtube.readonly`. The loopback flow described here has **no such
ceiling**, so features that need a broader scope — thumbnail upload, for one —
remain available. Loopback is the more capable choice, not the fallback.

### What this does not do

The browser has to be able to reach `localhost`, which means the tunnel has to
be up and you have to be the one at the keyboard. This is a way to connect
*your* accounts to *your* box. It is not a way to let other people sign in to an
installation they do not have shell access to — for that the server needs a real
name, which is the previous section.

One cosmetic wrinkle: if `tls.hostname` is set while you are browsing through
the tunnel, the setup card warns that you are browsing `localhost` while the
server is configured as something else. In this flow that warning is expected
and can be ignored.

## The data directory

```
<dataDir>/
  polyemesis.db     configuration, destinations, credentials, the library index
  secret.key        decrypts stored OAuth tokens and client secrets
  recordings/       segments, stems/, clips/, exports/
  hls/              playout segments
  tls/              generated CA and certificates (dir 0700, keys 0600)
```

**Back this up, and treat it as secret material.** `secret.key` is what decrypts
your stored platform tokens; without it they are unrecoverable, and with it
anyone can use them.

Nothing outside this directory is written at runtime, which is also what makes
the container's single volume mount sufficient.

## Environment variables

**Two, both for Rumble chat**, and everything else is a file key or a flag. The
restraint is deliberate: with three mechanisms it stops being obvious which one
won, so a variable has to earn its place.

| Variable | What it does |
|---|---|
| `RUMBLE_CHAT_API_KEY` | The Rumble chat credential, read by `internal/chat/rumble.go`. It is **not** the stream key. It comes from rumble.com/account/livestream-api and lives in the environment because Rumble's chat API has no sign-in, so there is no account to store it against. Treat the URL as a secret — it is the whole credential |
| `RUMBLE_CHAT_CHANNEL` | Which channel to read, when the key alone does not say |

Both are read at startup by `internal/api/chat_wiring.go`. See
[PLATFORMS.md](PLATFORMS.md) for the Rumble setup.

For containers, pass flags in `command:`, or mount a `config.yaml`. The
[`docker-compose.yml`](../docker-compose.yml) in the repo shows both.

## See also

- [INSTALL.md](INSTALL.md) — getting it running on each platform
- [QUICKSTART.md](QUICKSTART.md) — first stream in about five minutes
- [TROUBLESHOOTING.md](TROUBLESHOOTING.md) — when it is running but wrong
- [TLS.md](TLS.md) — the long version: how `auto` resolves, worked configurations
  for all five deployments, trusting the self-signed CA, and reverse proxies
