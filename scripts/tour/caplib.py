"""Capture library for the product tour: CDP screencast with per-frame timestamps,
a time-paced eased cursor, measured element boxes. Reads the rig credential only through
shotlib.login (never echoed). Reader: capture scripts in this folder.
Every path is relative to this folder; raw captures go to raw/ (git-ignored)."""
import os, sys, time, json, base64, math, subprocess, collections
HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.abspath(os.path.join(HERE, "..", ".."))
sys.path.insert(0, HERE)
from shotlib import sync_playwright, login, BASE, AUTH  # noqa
RAW = os.path.join(HERE, "raw")
os.makedirs(RAW, exist_ok=True)
DSF = float(os.environ.get("TOUR_DSF", "1.5"))

CURSOR_JS = """
(() => {
  const mk = () => {
    if (document.getElementById('__cur')) return;
    const d = document.createElement('div'); d.id = '__cur';
    d.style.cssText = 'position:fixed;left:0;top:0;width:20px;height:20px;margin:-10px 0 0 -10px;border-radius:50%;background:#f4f1ea;' +
      'box-shadow:0 0 0 3px rgba(7,8,10,.55),0 3px 10px rgba(0,0,0,.55);pointer-events:none;z-index:2147483647;' +
      'transition:transform 90ms ease-out;will-change:transform;opacity:0';
    document.documentElement.appendChild(d);
    const pos = window.__curpos || [1500, 620];
    d.style.left = pos[0] + 'px'; d.style.top = pos[1] + 'px'; d.style.opacity = '1';
    addEventListener('mousemove', e => { d.style.left = e.clientX + 'px'; d.style.top = e.clientY + 'px'; window.__curpos = [e.clientX, e.clientY]; }, true);
    addEventListener('mousedown', () => { d.style.transform = 'scale(.7)'; }, true);
    addEventListener('mouseup', () => { d.style.transform = 'scale(1)'; }, true);
  };
  if (document.readyState === 'loading') addEventListener('DOMContentLoaded', mk); else mk();
})();
"""

def ease(u):
    return 4 * u * u * u if u < 0.5 else 1 - pow(-2 * u + 2, 3) / 2


def st(name):
    """Puts the running sports rig into a routing or publishing state (stop, routing-initial, routing-final, ...)."""
    script = os.path.join(REPO, "scripts", "demo-seed.sh")
    r = subprocess.run([script, "--scene", "sports", "--state", name], stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, text=True)
    if r.returncode != 0:
        sys.exit("caplib: demo-seed.sh --scene sports --state %s failed (%d); is the rig up?\n%s" % (name, r.returncode, r.stderr.strip()[-400:]))


class Take:
    def __init__(self, p, name, start_pos=(1500, 620)):
        if not os.path.exists(AUTH):
            login(p)
        self.name = name
        self.b = p.chromium.launch(channel="chrome")
        self.c = self.b.new_context(viewport={"width": 1920, "height": 1080}, device_scale_factor=DSF,
                                    storage_state=AUTH, color_scheme="dark")
        self.c.add_init_script(CURSOR_JS)
        self.pg = self.c.new_page()
        self.cdp = self.c.new_cdp_session(self.pg)
        self.frames = []
        self.acks = collections.deque()
        self.events = []
        self.boxes = {}
        self.cx, self.cy = start_pos
        self.rec = False
        self.t0 = None
        self.cdp.on("Page.screencastFrame", self._on_frame)

    def _on_frame(self, ev):
        self.frames.append((ev["metadata"]["timestamp"], ev["data"]))
        self.acks.append(ev["sessionId"])

    def pump(self):
        while self.acks:
            self.cdp.send("Page.screencastFrameAck", {"sessionId": self.acks.popleft()})

    def wait(self, ms):
        end = time.time() + ms / 1000
        while True:
            self.pump()
            left = end - time.time()
            if left <= 0:
                break
            self.pg.wait_for_timeout(min(16, left * 1000))

    def goto(self, path, settle=3500):
        self.pg.goto(BASE + path)
        self.pg.wait_for_selector("nav")
        self.pg.evaluate("p => { window.__curpos = p; }", [self.cx, self.cy])
        self.pg.mouse.move(self.cx, self.cy)
        self.wait(settle)

    def start(self):
        self.cdp.send("Page.startScreencast", {"format": "jpeg", "quality": 92, "everyNthFrame": 1,
                                                "maxWidth": int(1920 * DSF), "maxHeight": int(1080 * DSF)})
        self.rec = True
        self.t_start = time.time()
        self.wait(120)

    def now(self):
        """seconds since the first captured frame"""
        if not self.frames:
            return 0.0
        return time.time() - self.frames[0][0]

    def mark(self, name):
        self.events.append({"name": name, "t": round(self.now(), 3)})

    def box(self, name, loc):
        bb = loc.bounding_box()
        self.boxes.setdefault(name, []).append({"t": round(self.now(), 3), "box": [round(bb[k], 1) for k in ("x", "y", "width", "height")]})
        return bb

    def move(self, x, y, dur=0.8):
        x0, y0 = self.cx, self.cy
        t0 = time.time()
        while True:
            el = time.time() - t0
            if el >= dur:
                break
            u = ease(el / dur)
            self.pg.mouse.move(x0 + (x - x0) * u, y0 + (y - y0) * u)
            self.pump()
        self.pg.mouse.move(x, y)
        self.cx, self.cy = x, y
        self.pump()

    def click_xy(self, x, y, dur=0.8):
        self.move(x, y, dur)
        self.wait(110)
        self.pg.mouse.down()
        self.wait(130)
        self.pg.mouse.up()
        self.wait(180)

    def click(self, loc, dur=0.8, dx=0, dy=0):
        bb = loc.bounding_box()
        self.click_xy(bb["x"] + bb["width"] / 2 + dx, bb["y"] + bb["height"] / 2 + dy, dur)

    def scroll_main(self, y, dur=0.9):
        cur = self.pg.evaluate("document.querySelector('main').scrollTop")
        t0 = time.time()
        while True:
            el = time.time() - t0
            if el >= dur:
                break
            self.pg.evaluate("v => document.querySelector('main').scrollTo(0, v)", cur + (y - cur) * ease(el / dur))
            self.pump()
        self.pg.evaluate("v => document.querySelector('main').scrollTo(0, v)", y)
        self.wait(100)

    def finish(self):
        self.wait(150)
        t_end = time.time()
        self.cdp.send("Page.stopScreencast")
        self.wait(100)
        self.rec = False
        d = os.path.join(RAW, self.name)
        os.makedirs(d, exist_ok=True)
        for f in os.listdir(d):
            os.remove(os.path.join(d, f))
        t0 = self.frames[0][0]
        lines = []
        for i, (ts, data) in enumerate(self.frames):
            fn = "f%05d.jpg" % i
            with open(os.path.join(d, fn), "wb") as fh:
                fh.write(base64.b64decode(data))
            nxt = self.frames[i + 1][0] if i + 1 < len(self.frames) else t_end
            lines.append("file '%s'\nduration %.5f" % (fn, max(nxt - ts, 0.001)))
        lines.append("file '%s'" % ("f%05d.jpg" % (len(self.frames) - 1)))
        with open(os.path.join(d, "list.txt"), "w") as fh:
            fh.write("\n".join(lines) + "\n")
        dur = t_end - t0
        meta = {"name": self.name, "dsf": DSF, "frames": len(self.frames), "duration": round(dur, 3),
                "events": self.events, "boxes": self.boxes}
        with open(os.path.join(RAW, self.name + ".json"), "w") as fh:
            json.dump(meta, fh, indent=1)
        out = os.path.join(RAW, self.name + ".mp4")
        subprocess.run(["ffmpeg", "-y", "-loglevel", "error", "-f", "concat", "-safe", "0", "-i", os.path.join(d, "list.txt"),
                        "-vf", "fps=30,format=yuv420p", "-t", "%.3f" % dur, "-c:v", "libx264", "-preset", "medium", "-crf", "13",
                        "-g", "15", "-an", out], check=True)
        print("%s: %d frames in %.1fs = %.1f fps captured -> %s" % (self.name, len(self.frames), dur, len(self.frames) / dur, out))
        self.b.close()
        return meta
