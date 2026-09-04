# Documentation

The [README](../README.md) is the front page: what polyemesis is, what it
deliberately does not do, and how to get it running. These are the details.

## Getting running

| Document | For |
|---|---|
| [QUICKSTART.md](QUICKSTART.md) | First stream in about five minutes |
| [INSTALL.md](INSTALL.md) | Per-platform installation, including what to do when your distro's FFmpeg is too old |
| [CONFIGURATION.md](CONFIGURATION.md) | `config.yaml`, flags, and — more usefully — *which* settings live in a file and which live in the UI |
| [HOT-RELOAD.md](HOT-RELOAD.md) | Which settings drop a connection when you change them mid-stream, and which do not |
| [TLS.md](TLS.md) | Certificates, ACME, reverse proxies, HSTS, and the SSH tunnel |
| [HARDWARE.md](HARDWARE.md) | GPU encoding, what helps and what does not |

## Using it

| Document | For |
|---|---|
| [OBS.md](OBS.md) | Multitrack over SRT step by step, and the two configurations that are not it |
| [AUDIO-ROUTING.md](AUDIO-ROUTING.md) | Simple mode, the mix matrix, clip protection, loudness and delay |
| [RENDITIONS.md](RENDITIONS.md) | Shared video encodes, ref counting, presets and hardware encoders |
| [PLATFORMS.md](PLATFORMS.md) | What each platform's published API allows, and the OAuth app setup for the four that sign in |
| [SCHEDULED-BROADCAST.md](SCHEDULED-BROADCAST.md) | Going live from a file, on a schedule, with no encoder attached |
| [MONITORING.md](MONITORING.md) | Prometheus metrics, alerts, and automation with API tokens |
| [MQTT.md](MQTT.md) | Retained telemetry and Home Assistant discovery |
| [HOOKS.md](HOOKS.md) | Signed lifecycle webhooks — one POST per transition, for a script rather than a person |
| [API.md](API.md) | Every HTTP route, authentication, and a worked example |
| [DESIGN-SYSTEM.md](DESIGN-SYSTEM.md) | The palette, type scale and motion vocabulary the app and website share |
| [TROUBLESHOOTING.md](TROUBLESHOOTING.md) | Organised by what you observe |
| [FAQ.md](FAQ.md) | The questions that come up first |
| [UPGRADING.md](UPGRADING.md) | Migrations, rollback, version-specific notes |
| [RELEASE-RUNBOOK.md](RELEASE-RUNBOOK.md) | Cutting a release: rehearse it, what each gate refuses, and why a published release is not a deployed one |

## Understanding it

| Document | For |
|---|---|
| [ARCHITECTURE.md](ARCHITECTURE.md) | How the pieces fit and why they are separate |
| [DESIGN-ONE-PORT-ONLY.md](DESIGN-ONE-PORT-ONLY.md) | Why token-addressed SRT is the only SRT path, and what removing per-source ports costs |
| [DESIGN-ONE-PORT-INGEST.md](DESIGN-ONE-PORT-INGEST.md) | The original token-addressing design, superseded in part by the above |
| [MODULES.md](MODULES.md) | Inventory of every dependency: version, licence, and whether it ships in the binary |
| [DEPENDENCIES.md](DEPENDENCIES.md) | Why the significant ones were chosen, and what was rejected |

## Testing and quality

| Document | For |
|---|---|
| [TESTING.md](TESTING.md) | Every suite and how to run it |
| [TEST-STRATEGY.md](TEST-STRATEGY.md) | What is covered, what deliberately is not, and the gaps |

## Contributing

| Document | For |
|---|---|
| [CONTRIBUTING.md](../CONTRIBUTING.md) | Setup, conventions, and the constraints that are not negotiable |
| [SITE-DEPLOY.md](SITE-DEPLOY.md) | Publishing `web/` to polyemesis.com: the two secrets, the DNS records, and how to verify — all maintainer actions |
| [SECURITY.md](../SECURITY.md) | Reporting a vulnerability, and the threat model stated plainly |
| [CODE_OF_CONDUCT.md](../CODE_OF_CONDUCT.md) | |
| [CHANGELOG.md](../CHANGELOG.md) | |

---

Each page owns its subject and links rather than repeats. Where two pages would
both want a passage — the SRT `streamid`, say, or why HSTS is opt-in — one holds
it and the other links to the anchor. If you find the same paragraph in two
places, that is a bug worth an issue.
