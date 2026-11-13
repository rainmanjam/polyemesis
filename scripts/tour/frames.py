"""usage: frames.py <video>   one frame per shot edge and every 3 s into check/frames/ (git-ignored), for sheets.py."""
import json, subprocess, sys, glob, os
if len(sys.argv) != 2:
    sys.exit("usage: frames.py <video>")
src = os.path.abspath(sys.argv[1])
os.chdir(os.path.dirname(os.path.abspath(__file__)))
os.makedirs("check/frames", exist_ok=True)
for f in glob.glob("check/frames/*.png"): os.remove(f)
ED=json.load(open("edit.json"))
ORDER=["s1a","s1b","s2a","s2b","s2c","s3a","s3b","s3c"]
D=0.3; t=0; times=[]
for s in ORDER:
    times += [(t+0.05, s+"_first"), (t+ED[s]-D-0.05, s+"_last")]
    t+=ED[s]-D
total=float(subprocess.check_output(["ffprobe","-v","error","-show_entries","format=duration","-of","csv=p=0",src]))
for k in range(0,int(total),3): times.append((k+0.0, "t%02d"%k))
times.append((total-0.1,"end"))
times.sort()
for i,(tt,n) in enumerate(times):
    fn="check/frames/%03d_%s_%.1f.png"%(i,n,tt)
    subprocess.run(["ffmpeg","-y","-loglevel","error","-ss","%.2f"%tt,"-i",src,"-frames:v","1",fn],check=True)
print(len(times))
