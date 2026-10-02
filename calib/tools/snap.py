"""Snap approximate polylines onto painted-line ridges; returns refined sample points."""
import cv2, numpy as np

def tophat(img, k=61):
    g = cv2.cvtColor(img, cv2.COLOR_BGR2GRAY) if img.ndim==3 else img
    g = cv2.GaussianBlur(g,(5,5),0)
    se = cv2.getStructuringElement(cv2.MORPH_ELLIPSE,(k,k))
    return cv2.morphologyEx(g, cv2.MORPH_TOPHAT, se)

def sample_profile(R, c, n, half):
    """Bilinear samples of R along the normal n at center c, offsets -half..half."""
    ts = np.arange(-half, half+1, 1.0)
    pts = c[None,:] + ts[:,None]*n[None,:]
    x = pts[:,0]; y = pts[:,1]
    x0=np.floor(x).astype(int); y0=np.floor(y).astype(int)
    H,W = R.shape
    ok = (x0>=0)&(x0<W-1)&(y0>=0)&(y0<H-1)
    v = np.full(len(ts), np.nan)
    if ok.sum()==0: return ts, v
    x0=x0[ok]; y0=y0[ok]; fx=(x-np.floor(x))[ok]; fy=(y-np.floor(y))[ok]
    v[ok] = (R[y0,x0]*(1-fx)*(1-fy) + R[y0,x0+1]*fx*(1-fy) +
             R[y0+1,x0]*(1-fx)*fy + R[y0+1,x0+1]*fx*fy)
    return ts, v

def snap_polyline(img, verts, n_samples=40, half=None, k=61, min_resp=6.0):
    """verts: [(x,y),...] approximate points along one painted line (>=2).
    Returns Nx2 refined points on the ridge centerline."""
    R = tophat(img, k).astype(np.float32)
    V = np.asarray(verts, float)
    # arc-length parameterisation of the polyline
    seg = np.diff(V, axis=0); L = np.linalg.norm(seg, axis=1); cum = np.r_[0, np.cumsum(L)]
    ss = np.linspace(0, cum[-1], n_samples)
    out=[]
    for s in ss:
        i = np.clip(np.searchsorted(cum, s)-1, 0, len(seg)-1)
        u = (s-cum[i])/max(L[i],1e-9)
        c = V[i] + u*seg[i]
        d = seg[i]/max(L[i],1e-9); n = np.array([-d[1], d[0]])
        hw = half if half else max(8, int(0.05*cum[-1]))
        ts, v = sample_profile(R, c, n, hw)
        if np.all(np.isnan(v)): continue
        v = np.nan_to_num(v, nan=0.0)
        v = np.convolve(v, np.ones(5)/5, mode='same')
        j = int(np.argmax(v))
        if v[j] < min_resp: continue
        # sub-pixel centroid around the peak
        lo=max(0,j-hw//2); hi=min(len(v), j+hw//2+1)
        w = np.clip(v[lo:hi]-0.5*v[j], 0, None)
        if w.sum() <= 0: continue
        t = (ts[lo:hi]*w).sum()/w.sum()
        out.append(c + t*n)
    return np.array(out)

def fit_line_resid(P):
    mu = P.mean(0); _,_,Vt = np.linalg.svd(P-mu, full_matrices=False)
    r = (P-mu)@Vt[1]
    return mu, Vt[0], r
