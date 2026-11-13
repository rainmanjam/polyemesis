"""Playwright helpers for the tour capture. Dark theme is forced with color_scheme='dark'
on the browser context (emulates prefers-color-scheme: dark); the console follows the
system theme.

The console is at TOUR_BASE (default http://127.0.0.1:8097, the sports scene of
scripts/demo-seed.sh). The sign-in password is read from
${DEMO_WORK:-${TMPDIR:-/tmp}/polyemesis-demo-sports}/admin.pw and is never printed;
the session it produces is kept beside it, outside the repository."""
import json
import os
import sys

BASE = os.environ.get("TOUR_BASE", "http://127.0.0.1:8097").rstrip("/")
WORK = os.environ.get("DEMO_WORK") or os.path.join(os.environ.get("TMPDIR") or "/tmp", "polyemesis-demo-sports")
PW_FILE = os.path.join(WORK, "admin.pw")
AUTH = os.path.join(WORK, "tour-auth.json")

try:
    from playwright.sync_api import sync_playwright  # noqa: F401
except ImportError:
    sys.exit("shotlib: the playwright package is missing (pip install playwright); system Google Chrome is used as the browser")


def login(p):
    """Signs in through the real login form once; the session goes to AUTH (mode 600, never printed)."""
    if not os.path.exists(PW_FILE):
        sys.exit("shotlib: %s is missing; start the rig first: scripts/demo-seed.sh --scene sports --no-capture" % PW_FILE)
    with open(PW_FILE) as fh:
        pw = fh.read().strip()
    b = p.chromium.launch(channel="chrome")
    c = b.new_context(viewport={"width": 1920, "height": 1080}, color_scheme="dark")
    pg = c.new_page()
    pg.goto(BASE + "/")
    pg.get_by_label("Username").fill("admin")
    pg.get_by_label("Password").fill(pw)
    pg.get_by_role("button", name="Sign in").click()
    pg.wait_for_selector("nav", timeout=20000)
    state = c.storage_state()
    fd = os.open(AUTH, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w") as fh:
        json.dump(state, fh)
    b.close()


def session_works(p):
    """True when the saved session is accepted by the console (one authenticated request answers 200)."""
    if not os.path.exists(AUTH):
        return False
    b = p.chromium.launch(channel="chrome")
    try:
        c = b.new_context(storage_state=AUTH)
        return c.request.get(BASE + "/api/v1/auth/me").status == 200
    except Exception:
        return False
    finally:
        b.close()


def ensure_session(p):
    """Reuses the saved session only if it still works (a rig restart invalidates it); otherwise signs in again."""
    if session_works(p):
        return
    if os.path.exists(AUTH):
        os.remove(AUTH)
    login(p)


def context(p, record=None):
    ensure_session(p)
    b = p.chromium.launch(channel="chrome")
    c = b.new_context(viewport={"width": 1920, "height": 1080}, storage_state=AUTH, color_scheme="dark")
    return b, c
