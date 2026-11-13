"""Playwright helpers for the tour capture. Dark theme is forced with color_scheme='dark'
on the browser context (emulates prefers-color-scheme: dark); the console follows the
system theme.

The console is at TOUR_BASE (default http://127.0.0.1:8097, the sports scene of
scripts/demo-seed.sh). The sign-in password is read from
${DEMO_WORK:-${TMPDIR:-/tmp}/polyemesis-demo-sports}/admin.pw and is never printed;
the session it produces is kept beside it, outside the repository."""
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
    c.storage_state(path=AUTH)
    os.chmod(AUTH, 0o600)
    b.close()


def context(p, record=None):
    if not os.path.exists(AUTH):
        login(p)
    b = p.chromium.launch(channel="chrome")
    c = b.new_context(viewport={"width": 1920, "height": 1080}, storage_state=AUTH, color_scheme="dark")
    return b, c
