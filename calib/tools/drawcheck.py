"""Draw the fitted horizon, axis vanishing points and world-line curves on a frame."""
import cv2, numpy as np, sys, os, json
sys.path.insert(0,os.path.dirname(os.path.abspath(__file__)))
from cammodel import Cam

def great_circle_curve(cam, n, w, h, npts=2000):
    """Image curve of the plane with normal n (through camera centre)."""
    n = n/np.linalg.norm(n)
    a = np.array([1.,0,0])
    if abs(n@a)>0.9: a=np.array([0,1.,0])
    e1 = np.cross(n,a); e1/=np.linalg.norm(e1); e2=np.cross(n,e1)
    t = np.linspace(0,2*np.pi,npts)
    V = np.outer(np.cos(t),e1)+np.outer(np.sin(t),e2)
    V = V[V[:,2]>0.02]
    if len(V)==0: return np.zeros((0,2))
    P = cam.project(V)
    ok=(P[:,0]>-3000)&(P[:,0]<w+3000)&(P[:,1]>-3000)&(P[:,1]<h+3000)
    return P[ok]

def draw(img, cam, R, out, label=""):
    h,w = img.shape[:2]; vis=img.copy()
    # horizon = vanishing line of the ground plane (normal = world Z in camera frame)
    zc = R@np.array([0,0,1.])
    C = great_circle_curve(cam, zc, w, h)
    for i in range(1,len(C)):
        p,q = C[i-1],C[i]
        if np.hypot(*(q-p))<50: cv2.line(vis,tuple(np.int32(p)),tuple(np.int32(q)),(0,255,255),2)
    col=[(0,0,255),(0,255,0),(255,0,0)]; nm=['X','Y','Z']
    for j in range(3):
        d = R@np.eye(3)[j]
        for s in (1,-1):
            v = s*d
            if v[2] <= 0.02: continue
            p = cam.project(v[None,:])[0]
            if -2000<p[0]<w+2000 and -2000<p[1]<h+2000:
                cv2.circle(vis,tuple(np.int32(p)),14,col[j],3)
                cv2.putText(vis,f"VP{nm[j]}{'+' if s>0 else '-'}",tuple(np.int32(p)+np.int32([18,6])),
                            cv2.FONT_HERSHEY_SIMPLEX,0.8,col[j],2)
    cv2.putText(vis,label,(15,32),cv2.FONT_HERSHEY_SIMPLEX,0.8,(0,255,255),2)
    cv2.imwrite(out,vis)
