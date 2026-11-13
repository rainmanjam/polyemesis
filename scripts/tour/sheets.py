"""Contact sheets (six frames each) of check/frames/ from frames.py, into out/sheets/ (git-ignored)."""
import glob, subprocess, os
os.chdir(os.path.dirname(os.path.abspath(__file__)))
os.makedirs("out/sheets", exist_ok=True)
fs = sorted(glob.glob("check/frames/0*.png"))
for f in glob.glob("out/sheets/m*.png"): os.remove(f)
for i in range(0, len(fs), 6):
    grp = fs[i:i+6]
    args = ["ffmpeg","-y","-loglevel","error"]
    for g in grp: args += ["-i", g]
    n=len(grp)
    fc = "".join("[%d:v]scale=960:540[a%d];"%(k,k) for k in range(n)) + "".join("[a%d]"%k for k in range(n)) + "xstack=inputs=%d:layout=%s[o]"%(n, "|".join(["0_0","960_0","0_540","960_540","0_1080","960_1080"][:n]))
    subprocess.run(args+["-filter_complex",fc,"-map","[o]","-frames:v","1","out/sheets/m%02d.png"%(i//6)],check=True)
    print("out/sheets/m%02d.png"%(i//6), [os.path.basename(g)[:-4] for g in grp])
