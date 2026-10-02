"""Fit focal length, distortion and rotation from segments with known world axes."""
import numpy as np, sys, os
sys.path.insert(0,os.path.dirname(os.path.abspath(__file__)))
from scipy.optimize import least_squares
from cammodel import Cam, rotvec_to_R, R_to_rotvec, euler_from_R
from manhattan import normals
from vpfit import R_from_ypr

def fit_labeled(S, lab, w, h, f0, ypr0, free_k=1, cx=None, cy=None, fix_f=False,
                k0=(0.,0.,0.,0.), huber=2.0):
    """S: Nx4 segments, lab: N ints in {0,1,2} for world X,Y,Z."""
    cx = w/2 if cx is None else cx; cy = h/2 if cy is None else cy
    lab = np.asarray(lab); S = np.asarray(S,float)
    W = np.clip(np.hypot(S[:,2]-S[:,0], S[:,3]-S[:,1]),0,300)
    sw = np.sqrt(W/W.mean())
    k0 = np.array(k0,float)
    p0 = np.r_[([] if fix_f else [f0]), k0[:free_k], R_to_rotvec(R_from_ypr(*ypr0))]
    def build(p):
        i=0
        f = f0 if fix_f else p[0]
        if not fix_f: i=1
        k=np.zeros(4); k[:free_k]=p[i:i+free_k]; i+=free_k
        return Cam(f,cx,cy,k), rotvec_to_R(p[i:i+3])
    def resid(p):
        c,R = build(p)
        N = normals(c,S)
        U = (R@np.eye(3)).T            # rows = world axes in camera frame
        d = np.abs(np.einsum('ij,ij->i', N, U[lab]))
        return sw*d*f0                 # angular residual scaled by a FIXED focal
    sol = least_squares(resid, p0, method="trf", loss="huber", f_scale=huber,
                        max_nfev=8000, xtol=1e-14, ftol=1e-14)
    cam,R = build(sol.x)
    N = normals(cam,S); U=(R@np.eye(3)).T
    r = np.abs(np.einsum('ij,ij->i', N, U[lab]))*cam.f
    return cam, R, dict(rms_px=float(np.sqrt(np.mean(r**2))), max_px=float(r.max()),
                        per=[float(np.sqrt(np.mean(r[lab==j]**2))) if (lab==j).any() else None for j in range(3)],
                        f=float(cam.f), k=cam.k.tolist(), hfov=float(cam.hfov_deg(w)),
                        ypr=[float(x) for x in euler_from_R(R)], resid=r.tolist())
