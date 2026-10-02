"""Bird's-eye registration: warp the ground plane from two cameras and align them."""
import numpy as np, cv2, sys, os
sys.path.insert(0,os.path.dirname(os.path.abspath(__file__)))
from cammodel import Cam

def bev_sample(img, cam, R_cw, C, xr, yr, res=0.02):
    """Sample the ground plane z=0 into a top-down image.
    R_cw: car->camera, C: camera position in the car frame."""
    xs = np.arange(xr[0], xr[1], res)
    ys = np.arange(yr[0], yr[1], res)
    X, Y = np.meshgrid(xs, ys)                       # rows = y, cols = x
    P = np.stack([X, Y, np.zeros_like(X)], -1).reshape(-1,3)
    V = (P - C) @ R_cw.T
    ok = V[:,2] > 0.05
    uv = np.full((len(P),2), -1.0)
    if ok.any():
        uv[ok] = cam.project(V[ok])
    h,w = img.shape[:2]
    good = ok & (uv[:,0]>=0)&(uv[:,0]<w-1)&(uv[:,1]>=0)&(uv[:,1]<h-1)
    mapx = uv[:,0].reshape(X.shape).astype(np.float32)
    mapy = uv[:,1].reshape(X.shape).astype(np.float32)
    out = cv2.remap(img, mapx, mapy, cv2.INTER_LINEAR, borderValue=0)
    return out, good.reshape(X.shape)

def ncc(a, b, m):
    a=a[m].astype(np.float64); b=b[m].astype(np.float64)
    if len(a) < 200: return -1.0
    a-=a.mean(); b-=b.mean()
    da=np.sqrt((a*a).sum()); db=np.sqrt((b*b).sum())
    if da<1e-6 or db<1e-6: return -1.0
    return float((a*b).sum()/(da*db))

def prep(gray):
    g = cv2.GaussianBlur(gray,(0,0),1.2)
    return cv2.createCLAHE(2.0,(8,8)).apply(g)
