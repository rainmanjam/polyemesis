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
Enhanced RTMP's version dependency is real, and it has not been confirmed with
OBS itself as the publisher. See `notes/enhanced-rtmp-multitrack.md`.

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

There are none. Everything is a file key or a flag. This is a deliberate choice
— with three mechanisms it stops being obvious which one won.

For containers, pass flags in `command:`, or mount a `config.yaml`. The
[`docker-compose.yml`](../docker-compose.yml) in the repo shows both.

## See also

- [INSTALL.md](INSTALL.md) — getting it running on each platform
- [QUICKSTART.md](QUICKSTART.md) — first stream in about five minutes
- [TROUBLESHOOTING.md](TROUBLESHOOTING.md) — when it is running but wrong
- [TLS.md](TLS.md) — the long version: how `auto` resolves, worked configurations
  for all five deployments, trusting the self-signed CA, and reverse proxies
