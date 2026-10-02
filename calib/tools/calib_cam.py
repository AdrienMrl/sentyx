"""Per-camera Manhattan calibration: grid search over (yaw,pitch,roll,f) seeded
by nominal rig geometry, then nonlinear refinement."""
import cv2, numpy as np, sys, json, argparse, os
sys.path.insert(0,os.path.dirname(os.path.abspath(__file__)))
from cammodel import Cam, euler_from_R, R_to_rotvec, rotvec_to_R
from vpfit import R_from_ypr
from manhattan import detect_segments, normals, refine

NOMINAL = {"front":(0,0,0), "back":(180,-12,0),
           "left_pillar":(60,-6,0), "right_pillar":(-60,-6,0),
           "left_repeater":(158,-14,0), "right_repeater":(-158,-14,0)}
FRANGE  = {"front":(2400,4200), "back":(400,820), "left_pillar":(600,1300),
           "right_pillar":(600,1300), "left_repeater":(500,1150), "right_repeater":(500,1150)}

def load_mask(cam, w, h, mask_dir):
    p = os.path.join(mask_dir, f"{cam}_exclude.png")
    if os.path.exists(p):
        m = cv2.imread(p,0)
        if m.shape[:2]!=(h,w): m=cv2.resize(m,(w,h))
        return m>127
    return np.zeros((h,w),bool)

def score_grid(N, W, f_list, Ns, yaws, pitches, rolls, thresh):
    best=(-1,None,None)
    for f,Nf in zip(f_list,Ns):
        for y in yaws:
            for p in pitches:
                for r in rolls:
                    R = R_from_ypr(y,p,r)
                    dd = np.abs(Nf@R.T)
                    lab = dd.argmin(1); dm = dd.min(1)
                    ok = dm<thresh
                    sc = 0.0
                    for j in range(3):
                        sc += min(float(W[ok&(lab==j)].sum()), 2500.0)
                    if sc>best[0]: best=(sc,f,(y,p,r))
    return best

def run(img_paths, cam_name, mask_dir=None, min_len=40, thresh=0.008, out=None,
        span=(35,20,25), step=2.5, nf=18):
    imgs=[cv2.imread(p) for p in img_paths]
    h,w = imgs[0].shape[:2]
    excl = load_mask(cam_name,w,h,mask_dir) if mask_dir else np.zeros((h,w),bool)
    S=[]
    for im in imgs:
        s = detect_segments(im, min_len=min_len)
        if len(s)==0: continue
        mx = ((s[:,0]+s[:,2])/2).astype(int).clip(0,w-1); my=((s[:,1]+s[:,3])/2).astype(int).clip(0,h-1)
        s = s[~excl[my,mx]]
        S.append(s)
    S=np.vstack(S)
    W = np.clip(np.hypot(S[:,2]-S[:,0], S[:,3]-S[:,1]),0,250)
    ny,np_,nr = NOMINAL[cam_name]
    lo,hi = FRANGE[cam_name]
    f_list = np.linspace(lo,hi,nf)
    Ns = [normals(Cam(f,w/2,h/2,(0,0,0,0)), S) for f in f_list]
    yaws = np.arange(ny-span[0], ny+span[0]+1e-9, step)
    pitches = np.arange(np_-span[1], np_+span[1]+1e-9, step)
    rolls = np.arange(nr-span[2], nr+span[2]+1e-9, step)
    sc,f,ypr = score_grid(N=None, W=W, f_list=f_list, Ns=Ns, yaws=yaws, pitches=pitches,
                          rolls=rolls, thresh=thresh)
    cam = Cam(f, w/2, h/2, (0,0,0,0)); R = R_from_ypr(*ypr)
    for _ in range(4):
        cam, R = refine(cam, S, W, R, free_k=2, thresh=thresh)
    N = normals(cam,S); d = np.abs(N@R.T); lab=d.argmin(1); res=d.min(1)
    lab[res>=thresh]=-1
    ypr2 = euler_from_R(R)
    info = dict(cam=cam_name, f=float(cam.f), k=cam.k.tolist(), w=w, h=h,
                hfov=float(cam.hfov_deg(w)), n_seg=int(len(S)),
                inl_len=[float(W[lab==j].sum()) for j in range(3)],
                inliers=[int((lab==j).sum()) for j in range(3)],
                rms_px=float(np.sqrt(np.mean((res[lab>=0]*cam.f)**2))) if (lab>=0).any() else None,
                yaw=float(ypr2[0]), pitch=float(ypr2[1]), roll=float(ypr2[2]), R=R.tolist())
    if out:
        vis = imgs[0].copy(); col=[(0,0,255),(0,255,0),(255,0,0)]
        for s,l in zip(S,lab):
            if l<0: continue
            cv2.line(vis,(int(s[0]),int(s[1])),(int(s[2]),int(s[3])),col[l],2)
        cv2.putText(vis,f"X=red Y=green Z=blue f={cam.f:.0f} hfov={info['hfov']:.1f} ypr=({ypr2[0]:.1f},{ypr2[1]:.1f},{ypr2[2]:.1f})",
                    (15,32),cv2.FONT_HERSHEY_SIMPLEX,0.7,(0,255,255),2)
        cv2.imwrite(out, vis)
    return info

if __name__=="__main__":
    a=argparse.ArgumentParser(); a.add_argument("cam"); a.add_argument("imgs",nargs="+")
    a.add_argument("--out"); a.add_argument("--masks",default=None)
    a.add_argument("--minlen",type=float,default=40); a.add_argument("--thresh",type=float,default=0.008)
    p=a.parse_args()
    info=run(p.imgs,p.cam,mask_dir=p.masks,min_len=p.minlen,thresh=p.thresh,out=p.out)
    print(json.dumps({k:v for k,v in info.items() if k!="R"},indent=1))
