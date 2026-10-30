# polyemesis OBS script

`polyemesis_obs.py` is an OBS Python script. It reports three events to a
polyemesis install: OBS started streaming, OBS stopped streaming, and OBS
switched its program scene. Polyemesis then starts or stops the destinations
you picked and applies your scene routing rules.

Use it when polyemesis cannot reach OBS's own WebSocket server, for example
when polyemesis runs on a VPS with no tunnel back to the streaming PC. When
polyemesis can reach OBS on your LAN or over Tailscale, let polyemesis connect
to obs-websocket instead, and you do not need this script.

Setup, the token it needs and what polyemesis does with each event are in
[docs/OBS-BRIDGE.md](../../docs/OBS-BRIDGE.md#set-up-obs-reports-to-polyemesis).
In short:

1. Create an API token with the **obs** scope in polyemesis.
2. In OBS, set **Tools → Scripts → Python Settings** to a Python 3 install.
3. Add this file in **Tools → Scripts**. Then fill in the polyemesis address
   and the token, and, for a self-signed install, the CA from
   `GET /api/v1/tls/ca`.

The script uses only Python's standard library, so there is nothing to install
with pip.
