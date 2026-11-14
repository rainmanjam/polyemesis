"""Films the tour's shots, one take each. usage: shots_run.py <shot> [<shot>...]
Reader: the assembly step (raw/<shot>.mp4 + raw/<shot>.json).
Needs the sports rig: scripts/demo-seed.sh --scene sports --no-capture."""
import sys
SHOTS = ["1a", "1b", "2a", "2b", "2c", "3a", "3b", "3c"]
if len(sys.argv) < 2 or sys.argv[1] in ("-h", "--help") or any(a not in SHOTS for a in sys.argv[1:]):
    print("usage: shots_run.py <shot> [<shot>...]    shots: %s\n"
          "Needs the sports rig running (scripts/demo-seed.sh --scene sports --no-capture);\n"
          "TOUR_BASE sets the console URL (default http://127.0.0.1:8097), TOUR_DSF the device scale (default 1.5)." % " ".join(SHOTS))
    sys.exit(0 if len(sys.argv) > 1 and sys.argv[1] in ("-h", "--help") else 2)
import re, time
from caplib import *

TRACKS = ["English", "Español", "Português", "Français", "Arabic", "Crowd"]
DESTS = ["YouTube · " + n for n in TRACKS[:5]]


def overview_tab(t):
    tab = t.pg.get_by_role("tab", name="Overview")
    if tab.get_attribute("aria-selected") != "true":
        tab.click(); t.wait(600)


def edit_tab(t):
    tab = t.pg.get_by_role("tab", name="Edit one destination")
    if tab.get_attribute("aria-selected") != "true":
        tab.click(); t.wait(700)


def grid_boxes(t):
    for i, d in enumerate(DESTS):
        for j, tr in enumerate(TRACKS):
            loc = t.pg.get_by_role("button", name=re.compile("^%s to %s," % (re.escape(tr), re.escape(d)))).first
            t.box("cell_%d_%d" % (i, j), loc)


def shot_1a(p):
    st("stop"); st("routing-initial"); time.sleep(3)
    t = Take(p, "s1a")
    t.goto("/", 7500)
    t.scroll_main(150, 0.05)
    t.box("h264", t.pg.get_by_text(re.compile(r"^h264 1920")).first)
    lab = t.pg.get_by_text(re.compile(r"^audio tracks$", re.I)).first
    t.box("audio", lab.locator("xpath=.."))
    t.box("preview", t.pg.locator("video").first)
    t.start()
    t.wait(900)
    t.move(1000, 420, 1.1); t.wait(900)
    t.move(1230, 700, 1.0); t.mark("at_h264"); t.wait(2300)
    t.move(1330, 770, 0.9); t.mark("at_audio"); t.wait(2300)
    t.move(1250, 560, 1.0); t.wait(1900)
    return t.finish()


def shot_1b(p):
    st("stop"); st("routing-initial"); time.sleep(3)
    t = Take(p, "s1b")
    t.goto("/routing", 1500)
    edit_tab(t)
    t.pg.get_by_text(DESTS[0], exact=True).first.click(); t.wait(2500)
    t.scroll_main(80, 0.05); t.wait(300)
    for n in range(1, 7):
        t.box("chk%d" % n, t.pg.locator("[aria-label='Include track %d']" % n))
    t.start()
    t.wait(1200)
    t.move(1000, 790, 1.0); t.wait(1200)
    t.move(1560, 330, 1.0); t.wait(5000)
    t.move(1560, 640, 1.2); t.wait(1800)
    return t.finish()


def shot_2a(p):
    st("stop"); st("routing-initial"); time.sleep(3)
    t = Take(p, "s2a")
    t.goto("/routing", 1500)
    overview_tab(t); t.wait(1500)
    grid_boxes(t)
    t.start()
    t.wait(1000)
    t.move(900, 700, 1.0); t.wait(1500)
    t.move(760, 640, 1.4); t.wait(5000)
    return t.finish()


def shot_2b(p):
    st("stop"); st("routing-initial"); time.sleep(3)
    t = Take(p, "s2b")
    t.goto("/routing", 1500)
    edit_tab(t)
    t.pg.get_by_text(DESTS[0], exact=True).first.click(); t.wait(1500)
    t.scroll_main(80, 0.05); t.wait(300)
    for n in range(1, 7):
        t.box("chk%d" % n, t.pg.locator("[aria-label='Include track %d']" % n))
    t.start()
    t.wait(900)
    es = t.pg.get_by_text(DESTS[1], exact=True).first
    t.box("rail_es", es)
    t.click(es, 1.0, dx=60); t.mark("rail_click"); t.wait(1200)
    t.box("rail_es_sel", t.pg.get_by_text(DESTS[1], exact=True).first)
    for n in (1, 3, 4, 5):
        t.click(t.pg.locator("[aria-label='Include track %d']" % n), 0.8)
        t.mark("uncheck%d" % n); t.wait(450)
    t.box("rail_es_after", t.pg.get_by_text(DESTS[1], exact=True).first)
    t.move(1250, 700, 0.9); t.wait(2600)
    t.mark("save_move")
    sv = t.pg.get_by_role("button", name="Save changes")
    t.box("save", sv)
    t.click(sv, 1.0); t.mark("save_click")
    t.wait(800)
    t.pg.screenshot(path=os.path.join(RAW, "dbg_2b_after_save.png"))
    t.move(1300, 520, 0.9)
    # wait the toast out
    for _ in range(60):
        txt = t.pg.evaluate("document.body.innerText")
        if not re.search(r"Routing saved|Saved|saved", txt.split("Mute and gain")[0] if False else "") and "Routing saved" not in txt:
            break
        t.wait(250)
    t.mark("toast_gone")
    t.wait(2600)
    return t.finish()


def shot_2c(p):
    st("stop"); st("routing-final"); time.sleep(3)
    t = Take(p, "s2c")
    t.goto("/routing", 1500)
    overview_tab(t); t.wait(1500)
    grid_boxes(t)
    t.start()
    t.wait(800)
    t.move(700, 720, 1.2)
    t.wait(10000)
    return t.finish()


def shot_3a(p):
    st("stop"); st("routing-final"); time.sleep(3)
    t = Take(p, "s3a")
    t.goto("/", 7500)
    t.start()
    t.wait(1400)
    ys = t.pg.evaluate("""() => { const e=[...document.querySelectorAll('main *')].find(e=>/^Destinations\\s*\\d*$/.test(e.textContent.trim())&&e.children.length<3); const m=document.querySelector('main'); return e.getBoundingClientRect().top - m.getBoundingClientRect().top + m.scrollTop - 70; }""")
    t.scroll_main(ys, 1.3); t.wait(700)
    gl = t.pg.get_by_role("button", name="Go live").first
    t.box("golive", gl)
    t.click(gl, 1.0); t.mark("golive_click"); t.wait(2800)
    cf = t.pg.get_by_role("button", name="Go live · 5 ready")
    t.click(cf, 0.9); t.mark("confirm_click")
    t0 = time.time()
    while time.time() - t0 < 30:
        if t.pg.get_by_text(re.compile(r"5 of 5 live")).count() > 0:
            break
        t.wait(200)
    t.mark("all_live"); t.wait(2300)
    return t.finish()


def shot_3b(p):
    t = Take(p, "s3b")
    t.goto("/", 7000)
    t.box("bar", t.pg.get_by_text(re.compile(r"5 live . 0 stopped")).first)
    t.box("healthy", t.pg.get_by_text("All 5 running destinations healthy").first)
    t.start()
    t.wait(1000)
    t.move(1100, 620, 1.2)
    t.wait(5500)
    return t.finish()


def shot_3c(p):
    t = Take(p, "s3c")
    t.goto("/routing/1", 3500)
    edit_tab(t)
    t.pg.get_by_text(DESTS[0], exact=True).first.click(); t.wait(3500)
    t.scroll_main(80, 0.05); t.wait(300)
    def meas(tag):
        lab = t.pg.get_by_text("Output, after routing", exact=True).first
        t.box("out_" + tag, lab.locator("xpath=.."))
        t.box("rail_" + tag, t.pg.get_by_text(DESTS[0 if tag == "en" else 1], exact=True).first)
    meas("en")
    t.start()
    t.wait(900)
    t.move(1400, 520, 1.0); t.wait(4600)
    es = t.pg.get_by_text(DESTS[1], exact=True).first
    t.click(es, 1.0, dx=60); t.mark("es_click"); t.wait(3200)
    meas("es")
    t.move(1400, 560, 0.9); t.wait(4500)
    return t.finish()


def console_up():
    import socket
    from urllib.parse import urlparse
    u = urlparse(BASE)
    try:
        socket.create_connection((u.hostname, u.port or 80), 3).close()
        return True
    except OSError:
        return False


if __name__ == "__main__":
    if not console_up():
        sys.exit("shots_run: no console at %s; start the rig first: scripts/demo-seed.sh --scene sports --no-capture" % BASE)
    with sync_playwright() as p:
        for name in sys.argv[1:]:
            globals()["shot_" + name](p)
