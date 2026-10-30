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
header of requests to the address you configure.
"""

import json
import queue
import ssl
import threading
import urllib.error
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


def script_update(settings):
    global _server, _token, _ca_file, _send_scenes
    _server = obs.obs_data_get_string(settings, "server").strip().rstrip("/")
    _token = obs.obs_data_get_string(settings, "token").strip()
    _ca_file = obs.obs_data_get_string(settings, "ca_file").strip()
    _send_scenes = obs.obs_data_get_bool(settings, "send_scenes")


def script_load(settings):
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
    return ssl.create_default_context()


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
        except Exception:  # noqa: BLE001 -- best effort, the status is the message
            pass
        print("polyemesis refused %s: HTTP %d %s" % (body.get("event"), e.code, detail))
        return e.code < 500
    except (urllib.error.URLError, OSError) as e:
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
