"""polyemesis OBS bridge script.

Tells a polyemesis install when OBS starts streaming, stops streaming, or
switches its program scene, by POSTing to /api/v1/obs/events. Use it when
polyemesis cannot reach OBS's own WebSocket server -- polyemesis on a VPS with
no tunnel back to the streaming PC. When polyemesis CAN reach obs-websocket,
let it connect instead (Settings, OBS bridge) and do not install this script;
running both is harmless, because polyemesis acts on changes and ignores a fact
it has already been told, but it is one more thing to configure.

Install: OBS, Tools, Scripts, Python Settings (point it at a Python 3
install), then + and choose this file. Fill in the polyemesis address and an
API token with the "obs" scope. See docs/OBS-BRIDGE.md.

THE TOKEN IS STORED IN PLAIN TEXT. OBS keeps script settings in the scene
collection's JSON file. That is why the token this script asks for is an "obs"
token: it can report these three facts and do nothing else -- it cannot read a
stream key, change a setting, or start a destination directly.

This script never prints the token, and sends it only in the Authorization
header of requests to the address you configure -- and only when that address
is https://, or http:// to this machine. Any other address is refused and
nothing is sent, the same rule the Stream Deck plugin applies.
"""

import json
import os
import queue
import ssl
import threading
import urllib.error
import urllib.parse
import urllib.request

import obspython as obs

# Settings, filled in by script_update.
_server = ""
_token = ""
_ca_file = ""
_send_scenes = True

# Requests are sent from a worker thread, never from OBS's frontend callback:
# that callback runs on OBS's UI thread, and a polyemesis that is slow to answer
# must not freeze OBS while the operator is trying to go live.
_queue = queue.Queue(maxsize=64)
_worker = None
_stop = threading.Event()

TIMEOUT_SECONDS = 5

# Where a system CA bundle lives, tried in order when Python's own store is
# empty: macOS and many Linux distros, Debian/Ubuntu, RHEL.
SYSTEM_CA_BUNDLES = (
    "/etc/ssl/cert.pem",
    "/etc/ssl/certs/ca-certificates.crt",
    "/etc/pki/tls/certs/ca-bundle.crt",
)
_bundle_logged = False
RETRIES = 2


def script_description():
    return (
        "<b>polyemesis OBS bridge</b><br>"
        "Reports stream start, stream stop and scene switches to polyemesis, "
        "which starts and stops your destinations and applies scene routing rules. "
        "Use an API token with the <i>obs</i> scope."
    )


def script_properties():
    props = obs.obs_properties_create()
    obs.obs_properties_add_text(props, "server", "polyemesis address (https://host:8080)", obs.OBS_TEXT_DEFAULT)
    obs.obs_properties_add_text(props, "token", "API token (obs scope)", obs.OBS_TEXT_PASSWORD)
    obs.obs_properties_add_path(
        props, "ca_file", "CA certificate (self-signed installs)", obs.OBS_PATH_FILE, "PEM (*.pem *.crt)", None
    )
    obs.obs_properties_add_bool(props, "send_scenes", "Report scene switches")
    return props


def script_defaults(settings):
    obs.obs_data_set_default_bool(settings, "send_scenes", True)


# The hosts plain http:// may be used with: this machine only.
LOOPBACK = {"localhost", "127.0.0.1", "::1"}


def parse_server(raw):
    """Return (address, refusal). The address is usable only if refusal is None.

    THE TOKEN GOES ONLY WHERE IT CANNOT BE READ ON THE WAY (poka-yoke audit
    2026-11-04). The script used to send the bearer token to whatever was typed,
    so http://my-vps:8080 put the token and every scene name on the network in
    the clear. Mirrors integrations/streamdeck/src/config.ts parseServerUrl:
    https:// to anywhere, http:// only to this machine.
    """
    text = (raw or "").strip().rstrip("/")
    if not text:
        return "", None
    try:
        url = urllib.parse.urlsplit(text)
        host = url.hostname
    except ValueError:
        return "", "the address is not a URL; use https://host:port"
    if not host:
        return "", "the address has no host; use https://host:port"
    if url.scheme == "https":
        return text, None
    if url.scheme == "http" and host in LOOPBACK:
        return text, None
    return "", (
        "refusing %s://%s: the API token would cross the network unencrypted. "
        "Use https://, or http:// only to this machine (localhost)" % (url.scheme or "?", host)
    )


def script_update(settings):
    global _server, _token, _ca_file, _send_scenes
    _server, refusal = parse_server(obs.obs_data_get_string(settings, "server"))
    if refusal:
        print("polyemesis: " + refusal)
    _token = obs.obs_data_get_string(settings, "token").strip()
    _ca_file = obs.obs_data_get_string(settings, "ca_file").strip()
    _send_scenes = obs.obs_data_get_bool(settings, "send_scenes")


def script_load(_settings):
    # OBS calls this positionally with the script's settings; the callback
    # signature is fixed by OBS, so the parameter stays even though unused.
    global _worker
    _stop.clear()
    _worker = threading.Thread(target=_run, name="polyemesis-obs", daemon=True)
    _worker.start()
    obs.obs_frontend_add_event_callback(on_event)


def script_unload():
    _stop.set()
    try:
        _queue.put_nowait(None)
    except queue.Full:
        pass


def on_event(event):
    if event == obs.OBS_FRONTEND_EVENT_STREAMING_STARTED:
        _enqueue({"event": "stream.started"})
    elif event == obs.OBS_FRONTEND_EVENT_STREAMING_STOPPED:
        _enqueue({"event": "stream.stopped"})
    elif event == obs.OBS_FRONTEND_EVENT_SCENE_CHANGED and _send_scenes:
        scene = obs.obs_frontend_get_current_scene()
        if scene is None:
            return
        try:
            name = obs.obs_source_get_name(scene)
        finally:
            obs.obs_source_release(scene)
        if name:
            _enqueue({"event": "scene.changed", "scene": name})


def _enqueue(body):
    try:
        _queue.put_nowait(body)
    except queue.Full:
        print("polyemesis: too many reports queued; dropping %s" % body.get("event"))


def _context():
    if not _server.startswith("https://"):
        return None
    if _ca_file:
        return ssl.create_default_context(cafile=_ca_file)
    return _default_context()


def _default_context():
    """The default context, unless Python's store has no CA certificates.

    The python.org macOS Python that OBS loads ships with an empty store, so
    every https report failed with "unable to get local issuer certificate".
    Then use certifi's bundle if it is installed, else the first system bundle.
    Verification is never switched off."""
    ctx = ssl.create_default_context()
    if ctx.cert_store_stats().get("x509_ca", 0) > 0:
        return ctx
    candidates = []
    try:
        import certifi

        candidates.append(("certifi", certifi.where()))
    # certifi is optional, so its absence is not an error.
    except Exception:  # noqa: BLE001
        pass
    candidates += [("system", p) for p in SYSTEM_CA_BUNDLES if os.path.isfile(p)]
    for kind, path in candidates:
        try:
            fallback = ssl.create_default_context(cafile=path)
        # ssl.SSLError is a subclass of OSError, so OSError alone covers both.
        except OSError:
            continue
        _log_bundle_once(kind, path)
        return fallback
    return ctx


def _log_bundle_once(kind, path):
    global _bundle_logged
    if not _bundle_logged:
        _bundle_logged = True
        print("polyemesis: Python has no CA certificates of its own; verifying with the %s bundle %s" % (kind, path))


def _send(body):
    """POST one fact. Returns True when polyemesis accepted it."""
    if not _server or not _token:
        print("polyemesis: set the address and token in the script's settings")
        return True  # nothing will change by retrying
    req = urllib.request.Request(
        _server + "/api/v1/obs/events",
        data=json.dumps(body).encode("utf-8"),
        method="POST",
        headers={"Content-Type": "application/json", "Authorization": "Bearer " + _token},
    )
    try:
        with urllib.request.urlopen(req, timeout=TIMEOUT_SECONDS, context=_context()) as resp:
            resp.read()
            return True
    except urllib.error.HTTPError as e:
        # A 4xx will not change on retry: the token is wrong, the bridge is
        # off, or the body was refused. Say which, without the token.
        detail = ""
        try:
            detail = json.loads(e.read().decode("utf-8")).get("error", "")
        # Best effort: the status is the message, so a body we cannot parse is fine.
        except Exception:  # noqa: BLE001
            pass
        print("polyemesis refused %s: HTTP %d %s" % (body.get("event"), e.code, detail))
        return e.code < 500
    # URLError is a subclass of OSError (HTTPError was caught above), so OSError
    # covers the connection failures.
    except OSError as e:
        print("polyemesis unreachable for %s: %s" % (body.get("event"), getattr(e, "reason", e)))
        return False


def _run():
    # Drains what was queued before an unload, one attempt each, so a stream
    # stop reported as OBS closes still reaches polyemesis; only the retries
    # are cut short.
    while True:
        body = _queue.get()
        if body is None:
            return
        for attempt in range(RETRIES + 1):
            if _send(body) or _stop.is_set():
                break
            _stop.wait(2 * (attempt + 1))
