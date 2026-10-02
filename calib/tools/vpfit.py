"""Manhattan-world calibration: fit focal length, distortion and rotation from
sets of image lines known to be parallel to the world X / Y / Z axes.

World frame: X = longitudinal (car forward), Y = lateral (car left), Z = up.
"""
import numpy as np, json
from scipy.optimize import least_squares
from cammodel import Cam, rotvec_to_R, R_to_rotvec, euler_from_R

AXES = {"x":np.array([1.,0,0]), "y":np.array([0,1.,0]), "z":np.array([0,0,1.])}

def line_residuals(rays, u):
    """Angular distance of each ray from the best plane containing direction u."""
    e = np.eye(3) - np.outer(u,u)
    w,V = np.linalg.eigh(e)
    B = V[:, w > 0.5]                      # 3x2 basis of u-perp
    M = B.T @ (rays.T @ rays) @ B          # 2x2
    w2,V2 = np.linalg.eigh(M)
    n = B @ V2[:,0]
    n = n/np.linalg.norm(n)
    return rays @ n

def pack(cam, rvec, free_k):
    return np.r_[cam.f, cam.k[:free_k], rvec]

def unpack(p, cx, cy, free_k):
    k = np.zeros(4); k[:free_k] = p[1:1+free_k]
    return Cam(p[0], cx, cy, k), p[1+free_k:1+free_k+3]

def fit(lines, w, h, f0, r0, free_k=2, cx=None, cy=None, huber=None):
    """lines: list of dicts {axis: 'x'|'y'|'z', pts: Nx2}. Returns cam, R, info."""
    cx = w/2 if cx is None else cx; cy = h/2 if cy is None else cy
    k0 = np.array([1/3, 2/15, 17/315, 62/2835])
    cam0 = Cam(f0, cx, cy, k0)
    p0 = pack(cam0, np.asarray(r0,float), free_k)
    P = [np.asarray(l["pts"],float) for l in lines]
    A = [AXES[l["axis"]] for l in lines]
    Wt = [float(l.get("w",1.0)) for l in lines]

    def resid(p):
        cam, rv = unpack(p, cx, cy, free_k)
        R = rotvec_to_R(rv)
        out=[]
        for pts, ax, wt in zip(P, A, Wt):
            rays = cam.unproject(pts)
            u = R @ ax
            r = line_residuals(rays, u) * wt
            out.append(r/np.sqrt(len(pts)))
        return np.concatenate(out)

    sol = least_squares(resid, p0, method="lm" if huber is None else "trf",
                        loss="linear" if huber is None else "huber",
                        f_scale=huber if huber else 1.0, max_nfev=20000, xtol=1e-14, ftol=1e-14)
    cam, rv = unpack(sol.x, cx, cy, free_k)
    R = rotvec_to_R(rv)
    per=[]
    for pts, ax, l in zip(P, A, lines):
        rays = cam.unproject(pts); u = R @ ax
        r = line_residuals(rays, u)
        # convert angular residual to pixels at that image location
        per.append(dict(name=l.get("name",""), axis=l["axis"], n=len(pts),
                        rms_px=float(np.sqrt(np.mean(r**2))*cam.f),
                        max_px=float(np.max(np.abs(r))*cam.f)))
    info=dict(cost=float(sol.cost), f=cam.f, k=cam.k.tolist(),
              hfov=float(cam.hfov_deg(w)), per_line=per,
              rms_px=float(np.sqrt(np.mean(np.concatenate([
                  (line_residuals(cam.unproject(pts), R@ax))**2 for pts,ax in zip(P,A)])))*cam.f))
    return cam, R, info

def R_from_ypr(yaw,pitch,roll):
    """World-frame yaw/pitch/roll (deg) -> R_cw (world->camera)."""
    y,p,r=np.radians([yaw,pitch,roll])
    z=np.array([np.cos(p)*np.cos(y), np.cos(p)*np.sin(y), np.sin(p)])
    up=np.array([0,0,1.0]); up=up-z*np.dot(up,z); up/=np.linalg.norm(up)
    right=np.cross(z,up)                    # image +x
    x=np.cos(r)*right+np.sin(r)*(-up)       # roll about the optical axis
    ydir=np.cross(z,x)                      # image +y (down)
    return np.vstack([x,ydir,z])

def fit_multistart(lines, w, h, nominal, f_grid, ypr_span=(20,14,8), n=(5,5,3), free_k=2, **kw):
    """nominal=(yaw,pitch,roll) deg. Coarse grid then LM refine; keeps best."""
    best=None
    ys=np.linspace(nominal[0]-ypr_span[0], nominal[0]+ypr_span[0], n[0])
    ps=np.linspace(nominal[1]-ypr_span[1], nominal[1]+ypr_span[1], n[1])
    rs=np.linspace(nominal[2]-ypr_span[2], nominal[2]+ypr_span[2], n[2])
    for f0 in np.atleast_1d(f_grid):
        for y in ys:
            for p in ps:
                for r in rs:
                    r0 = R_to_rotvec(R_from_ypr(y,p,r))
                    try:
                        cam,R,info = fit(lines,w,h,f0,r0,free_k=1,**kw)
                    except Exception: continue
                    if best is None or info["rms_px"] < best[2]["rms_px"]:
                        best=(cam,R,info)
    cam,R,info = best
    if free_k>1:
        cam2,R2,info2 = fit(lines,w,h,cam.f,R_to_rotvec(R),free_k=free_k,**kw)
        if info2["rms_px"] <= info["rms_px"]*1.02: return cam2,R2,info2
    return cam,R,info
