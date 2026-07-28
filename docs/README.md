# Documentation

Start with the [README](../README.md) — it is the tour. These are the details.

## Getting running

| Document | For |
|---|---|
| [QUICKSTART.md](QUICKSTART.md) | First stream in about five minutes |
| [INSTALL.md](INSTALL.md) | Per-platform installation, including what to do when your distro's FFmpeg is too old |
| [CONFIGURATION.md](CONFIGURATION.md) | `config.yaml`, flags, and — more usefully — *which* settings live in a file and which live in the UI |
| [HARDWARE.md](HARDWARE.md) | GPU encoding, what helps and what does not |

## Using it

| Document | For |
|---|---|
| [API.md](API.md) | Every HTTP route, authentication, and a worked example |
| [TROUBLESHOOTING.md](TROUBLESHOOTING.md) | Organised by what you observe |
| [FAQ.md](FAQ.md) | The questions that come up first |
| [UPGRADING.md](UPGRADING.md) | Migrations, rollback, version-specific notes |

**OBS setup** and **streaming platform support** are covered in the README
rather than here — see [OBS setup](../README.md#obs-setup),
[Streaming platform support](../README.md#streaming-platform-support) and
[Platform accounts](../README.md#platform-accounts). They are deliberately not
duplicated: two copies of setup instructions is two copies to drift.

## Understanding it

| Document | For |
|---|---|
| [ARCHITECTURE.md](ARCHITECTURE.md) | How the pieces fit and why they are separate |
| [DESIGN-ONE-PORT-INGEST.md](DESIGN-ONE-PORT-INGEST.md) | Token-addressed ingest, and where it improves on the design that inspired it |
| [MODULES.md](MODULES.md) | Inventory of every dependency: version, licence, and whether it ships in the binary |
| [DEPENDENCIES.md](DEPENDENCIES.md) | Why the significant ones were chosen, and what was rejected |
| [RESEARCH-COMPETITIVE.md](RESEARCH-COMPETITIVE.md) | What comparable tools do, and the gaps that shaped this one |

## Testing and quality

| Document | For |
|---|---|
| [TESTING.md](TESTING.md) | Every suite and how to run it |
| [TEST-STRATEGY.md](TEST-STRATEGY.md) | What is covered, what deliberately is not, and the gaps |
| [REVIEW-POKA-YOKE.md](REVIEW-POKA-YOKE.md) | A mistake-proofing review of the code and UI, and the eight changes it produced |

## Contributing

| Document | For |
|---|---|
| [CONTRIBUTING.md](../CONTRIBUTING.md) | Setup, conventions, and the constraints that are not negotiable |
| [SECURITY.md](../SECURITY.md) | Reporting a vulnerability, and the threat model stated plainly |
| [CODE_OF_CONDUCT.md](../CODE_OF_CONDUCT.md) | |
| [CHANGELOG.md](../CHANGELOG.md) | |
