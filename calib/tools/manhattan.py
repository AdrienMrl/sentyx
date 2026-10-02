"""Automatic Manhattan-frame calibration from LSD segments.

A segment's two endpoints, unprojected, span a plane through the camera centre
that contains the world line, so short chords of a curved (fisheye) line are
still exact constraints. Family j requires n . (R e_j) = 0.
"""
import cv2, numpy as np
from cammodel import Cam, rotvec_to_R, R_to_rotvec
from scipy.optimize import least_squares

def _lsd(g, min_len, scale):
    if scale!=1.0: g = cv2.resize(g,None,fx=scale,fy=scale)
    lines,_,_,_ = cv2.createLineSegmentDetector().detect(g)
    if lines is None: return np.zeros((0,4))
    S = lines.reshape(-1,4)/scale
    L = np.hypot(S[:,2]-S[:,0], S[:,3]-S[:,1])
    return S[L>=min_len]

def detect_segments(img, min_len=25, scale=1.0, clahe=True, paint=True):
    g0 = cv2.cvtColor(img, cv2.COLOR_BGR2GRAY) if img.ndim==3 else img
    g = cv2.createCLAHE(3.0,(8,8)).apply(g0) if clahe else g0
    out=[_lsd(g, min_len, scale)]
    if paint:
        # enhance faded road markings: large-kernel top-hat, then stretch
        se = cv2.getStructuringElement(cv2.MORPH_ELLIPSE,(81,81))
        th = cv2.morphologyEx(cv2.GaussianBlur(g0,(5,5),0), cv2.MORPH_TOPHAT, se)
        th = cv2.normalize(th,None,0,255,cv2.NORM_MINMAX).astype(np.uint8)
        out.append(_lsd(th, min_len, scale))
    return np.vstack(out)

def normals(cam, S):
    v1 = cam.unproject(S[:,:2]); v2 = cam.unproject(S[:,2:])
    n = np.cross(v1,v2)
    ln = np.linalg.norm(n,axis=1,keepdims=True)
    return n/np.maximum(ln,1e-12)

def ransac_frame(N, W, thresh=0.01, iters=30000, seed=0):
    """Find 3 orthogonal directions explaining the most segment normals."""
    rng = np.random.default_rng(seed); M=len(N)
    best=(None,-1)
    if M < 8: return None, None
    ia,ib,ic,idd = (rng.integers(0,M,(4,iters)))
    for t in range(iters):
        d1 = np.cross(N[ia[t]], N[ib[t]])
        n1 = np.linalg.norm(d1)
        if n1 < 1e-6: continue
        d1/=n1
        d2 = np.cross(N[ic[t]], N[idd[t]])
        n2 = np.linalg.norm(d2)
        if n2 < 1e-6: continue
        d2/=n2
        d2 = d2 - d1*np.dot(d1,d2)
        n2 = np.linalg.norm(d2)
        if n2 < 0.35: continue      # the two VPs must be far from parallel
        d2/=n2
        d3 = np.cross(d1,d2)
        D = np.vstack([d1,d2,d3])
        r = np.abs(N@D.T).min(1)
        sc = (W*(r<thresh)).sum()
        if sc > best[1]: best=(D,sc)
    return best

def assign(N, D, thresh):
    r = np.abs(N@D.T)
    lab = r.argmin(1); res = r.min(1)
    lab[res>=thresh] = -1
    return lab, res

def refine(cam, S, W, D, free_k=2, thresh=0.01, cx=None, cy=None):
    """Nonlinear refinement of f, k and the frame using inlier segments."""
    cx = cam.cx if cx is None else cx; cy = cam.cy if cy is None else cy
    R0 = D.copy()
    if np.linalg.det(R0) < 0: R0[2] = -R0[2]
    p0 = np.r_[cam.f, cam.k[:free_k], R_to_rotvec(R0)]
    def build(p):
        k=np.zeros(4); k[:free_k]=p[1:1+free_k]
        c=Cam(p[0],cx,cy,k); R=rotvec_to_R(p[1+free_k:1+free_k+3])
        return c,R
    sw = np.sqrt(W/np.mean(W))
    def resid(p):
        c,R = build(p)
        N = normals(c,S)
        m = np.abs(N@R.T).min(1)
        return sw*m/thresh
    sol = least_squares(resid, p0, method="trf", loss="soft_l1", f_scale=1.0,
                        max_nfev=4000, xtol=1e-12, ftol=1e-12)
    c,R = build(sol.x)
    return c,R
