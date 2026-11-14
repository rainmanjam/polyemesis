"""Trims / joins / speeds the raw shots into hf/assets/shots/*.mp4 and writes edit.json (durations)."""
import subprocess, json, os, shutil
HERE = os.path.dirname(os.path.abspath(__file__))
os.chdir(HERE)
FONTS = os.path.join(HERE, "..", "..", "web", "public", "fonts")
# source file in web/public/fonts -> name the compositions use in hf/assets/fonts
FONT_FILES = {"bricolage-grotesque.woff2": "bricolage.woff2", "instrument-serif-italic.woff2": "instrument-serif-italic.woff2",
              "jetbrains-mono.woff2": "jetbrains-mono.woff2"}
os.makedirs("hf/assets/fonts", exist_ok=True)
for src, dst in FONT_FILES.items():
    shutil.copyfile(os.path.join(FONTS, src), os.path.join("hf/assets/fonts", dst))
EDITS = {
 "s1a": [(0.3, 11.3, 1)], "s1b": [(0.2, 11.4, 1)], "s2a": [(0.2, 8.6, 1)],
 "s2b": [(1.0, 18.1, 1), (21.8, 24.3, 1)], "s2c": [(0.2, 9.2, 1)],
 "s3a": [(3.0, 10.3, 1), (10.3, 21.3, 2), (21.3, 23.6, 1)],
 "s3b": [(0.3, 6.3, 1)], "s3c": [(0.9, 15.0, 1)],
}
out = {}
os.makedirs("hf/assets/shots", exist_ok=True)
for name, segs in EDITS.items():
    parts = []
    for i, (a, b, sp) in enumerate(segs):
        parts.append("[0:v]trim=start=%s:end=%s,setpts=(PTS-STARTPTS)/%s,fps=30[v%d]" % (a, b, sp, i))
    fc = ";".join(parts) + ";" + "".join("[v%d]" % i for i in range(len(segs))) + "concat=n=%d:v=1:a=0[o]" % len(segs)
    dst = "hf/assets/shots/%s.mp4" % name
    subprocess.run(["ffmpeg", "-y", "-loglevel", "error", "-i", "raw/%s.mp4" % name, "-filter_complex", fc, "-map", "[o]",
                    "-c:v", "libx264", "-preset", "medium", "-crf", "14", "-pix_fmt", "yuv420p", "-g", "15", "-r", "30", "-an", dst], check=True)
    d = float(subprocess.check_output(["ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", dst]))
    out[name] = round(d, 3)
json.dump(out, open("edit.json", "w"), indent=1)
print(out)
