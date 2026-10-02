"""Identify the recording car's own bodywork in each camera view.

The body is the only content that is identical across events shot at different
places, so we compare per-event temporal medians with local normalised
cross-correlation.
"""
import cv2, numpy as np, sys, os, glob, json

def median_frame(path, n=25):
    cap = cv2.VideoCapture(path)
    total = int(cap.get(cv2.CAP_PROP_FRAME_COUNT))
    idx = np.linspace(0, max(total-2,0), n).astype(int)
    fr=[]
    for i in idx:
        cap.set(cv2.CAP_PROP_POS_FRAMES, int(i))
        ok, f = cap.read()
        if ok: fr.append(f)
    cap.release()
    return np.median(np.stack(fr),0).astype(np.uint8) if fr else None

def local_ncc(a, b, win=15):
    a = cv2.cvtColor(a, cv2.COLOR_BGR2GRAY).astype(np.float32)
    b = cv2.cvtColor(b, cv2.COLOR_BGR2GRAY).astype(np.float32)
    k = (win,win)
    ma = cv2.blur(a,k); mb = cv2.blur(b,k)
    va = cv2.blur(a*a,k)-ma*ma; vb = cv2.blur(b*b,k)-mb*mb
    cab = cv2.blur(a*b,k)-ma*mb
    ncc = cab/np.sqrt(np.maximum(va,1e-3)*np.maximum(vb,1e-3))
    tex = np.minimum(np.sqrt(np.maximum(va,0)), np.sqrt(np.maximum(vb,0)))
    return ncc, tex

if __name__=="__main__":
    root = sys.argv[1]; outdir = sys.argv[2]; os.makedirs(outdir, exist_ok=True)
    cams = ["front","back","left_pillar","right_pillar","left_repeater","right_repeater"]
    events = sorted(glob.glob(os.path.join(root,"pi-*")))
    for cam in cams:
        meds=[]
        for ev in events:
            f = glob.glob(os.path.join(ev, f"*-{cam}.mp4"))
            if not f: continue
            m = median_frame(f[0])
            if m is not None: meds.append((os.path.basename(ev), m))
        if len(meds) < 2: continue
        H,W = meds[0][1].shape[:2]
        acc = np.ones((H,W), np.float32)
        for i in range(len(meds)):
            for j in range(i+1, len(meds)):
                n_, t_ = local_ncc(meds[i][1], meds[j][1])
                acc = np.minimum(acc, np.where(t_ > 7.0, n_, -1.0))
        mask = (acc > 0.62).astype(np.uint8)*255
        mask = cv2.morphologyEx(mask, cv2.MORPH_OPEN, np.ones((9,9),np.uint8))
        mask = cv2.morphologyEx(mask, cv2.MORPH_CLOSE, np.ones((25,25),np.uint8))
        n,lab,st,_ = cv2.connectedComponentsWithStats(mask,8)
        keep = np.zeros_like(mask)
        for i in range(1,n):
            if st[i,cv2.CC_STAT_AREA] > 0.004*H*W: keep[lab==i]=255
        cv2.imwrite(f"{outdir}/{cam}_mask.png", keep)
        for name,m in meds: cv2.imwrite(f"{outdir}/{cam}_med_{name}.png", m)
        vis = meds[0][1].copy(); vis[keep>0] = (0.45*vis[keep>0] + 0.55*np.array([0,0,255])).astype(np.uint8)
        cv2.imwrite(f"{outdir}/{cam}_ownbody.png", vis)
        print(cam, "events",len(meds), "own-body px %", round(100*(keep>0).mean(),1))
