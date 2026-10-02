"""Ground-plane calibration from parallel line families plus vertical lines."""
import numpy as np, sys, os
sys.path.insert(0,os.path.dirname(os.path.abspath(__file__)))
from cammodel import Cam
from manhattan import normals

def family_dir(cam, S):
    """Best-fit 3D direction shared by a set of coplanar-with-centre segments."""
    N = normals(cam, S)
    W = np.clip(np.hypot(S[:,2]-S[:,0],S[:,3]-S[:,1]),0,400)
    M = (N*W[:,None]).T @ N
    w,V = np.linalg.eigh(M)
    d = V[:,0]
    res = np.abs(N@d)
    return d/np.linalg.norm(d), res

def solve_f(S_fams, S_vert, w, h, f_lo, f_hi, k1=0.0, n=400):
    """Pick f so the ground normal from the line families matches the vertical VP."""
    best=None; rows=[]
    for f in np.linspace(f_lo,f_hi,n):
        cam = Cam(f,w/2,h/2,(k1,0,0,0))
        ds=[family_dir(cam,S)[0] for S in S_fams]
        ng = np.cross(ds[0],ds[1]); ng/=np.linalg.norm(ng)
        dv,_ = family_dir(cam,S_vert)
        if np.dot(ng,dv)<0: dv=-dv
        ang = np.degrees(np.arccos(np.clip(np.dot(ng,dv),-1,1)))
        rows.append((f,ang))
        if best is None or ang<best[1]: best=(f,ang,ng,dv,cam)
    return best, np.array(rows)

def height_from_length(cam, up, p1, p2, L, z=0.0):
    """Camera height from two points at height z above ground separated by L."""
    from scipy.optimize import brentq
    d1=cam.unproject(np.array(p1,float)[None,:])[0]; d2=cam.unproject(np.array(p2,float)[None,:])[0]
    def g(hh):
        t=hh-z
        return np.linalg.norm(d1*(t/(-d1@up))-d2*(t/(-d2@up)))-L
    return brentq(g,0.2,4.0)
