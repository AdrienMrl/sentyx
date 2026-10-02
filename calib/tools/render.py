"""Project CGI geometry (ground grid, vehicle wireframes) into a real frame."""
import numpy as np, cv2, sys, os
sys.path.insert(0,os.path.dirname(os.path.abspath(__file__)))
from cammodel import Cam

def project_polyline(cam, P, w, h, max_step=120):
    """P: Nx3 points in camera frame -> list of pixel polylines (clipped to z>0)."""
    P=np.asarray(P,float)
    out=[]; cur=[]
    for p in P:
        if p[2] <= 0.05:
            if len(cur)>1: out.append(np.array(cur))
            cur=[]; continue
        uv=cam.project(p[None,:])[0]
        if cur and np.hypot(*(uv-cur[-1]))>max_step:
            if len(cur)>1: out.append(np.array(cur))
            cur=[]
        cur.append(uv)
    if len(cur)>1: out.append(np.array(cur))
    return out

def draw_polyline3(img, cam, P, color, thick=2, n=160):
    P=np.asarray(P,float)
    dense=[]
    for i in range(len(P)-1):
        t=np.linspace(0,1,n)[:,None]
        dense.append(P[i]*(1-t)+P[i+1]*t)
    if not dense: return
    D=np.vstack(dense)
    h,w=img.shape[:2]
    for seg in project_polyline(cam,D,w,h):
        pts=np.int32(seg)
        cv2.polylines(img,[pts],False,color,thick,cv2.LINE_AA)

def ground_grid(cam, img, R_cw, h_cam, color=(0,220,255), step=1.0,
                xr=(-12,12), yr=(-12,12), thick=1):
    """Grid on the ground plane, axes aligned with the car frame."""
    def to_cam(X,Y):
        P=np.stack([X,Y,np.full_like(X,-h_cam)],axis=-1)   # car-frame, camera at origin height h
        return P@R_cw.T
    for y in np.arange(yr[0],yr[1]+1e-9,step):
        X=np.linspace(xr[0],xr[1],400); Y=np.full_like(X,y)
        draw_polyline3(img,cam,to_cam(X,Y),color,thick)
    for x in np.arange(xr[0],xr[1]+1e-9,step):
        Y=np.linspace(yr[0],yr[1],400); X=np.full_like(Y,x)
        draw_polyline3(img,cam,to_cam(X,Y),color,thick)

def car_wireframe(L=4.720, W=1.849, H=1.441, wb=2.875, track=1.580,
                  front_overhang=0.840, wheel_r=0.334, roofline=0.62):
    """Simple dimensionally-accurate Model 3 wireframe in its own frame:
    origin on the ground at the wheelbase midpoint, x forward, y left, z up."""
    xf = wb/2; xr = -wb/2
    x_front = xf + front_overhang; x_rear = x_front - L
    yw = W/2
    body_h = 0.95*H
    edges=[]
    # lower body box
    corners=[(x_front,+yw,0.30),(x_front,-yw,0.30),(x_rear,-yw,0.30),(x_rear,+yw,0.30)]
    for i in range(4): edges.append([corners[i],corners[(i+1)%4]])
    belt=[(x_front-0.55,+yw,0.98),(x_front-0.55,-yw,0.98),(x_rear+0.50,-yw,0.98),(x_rear+0.50,+yw,0.98)]
    for i in range(4): edges.append([belt[i],belt[(i+1)%4]])
    for a,b in zip(corners,belt): edges.append([a,b])
    roof=[(xf-0.35,+roofline,H),(xf-0.35,-roofline,H),(xr+0.15,-roofline,H),(xr+0.15,+roofline,H)]
    for i in range(4): edges.append([roof[i],roof[(i+1)%4]])
    for a,b in zip(belt,roof): edges.append([a,b])
    # wheels as circles in vertical planes
    wheels=[]
    for sx in (xf,xr):
        for sy in (+track/2,-track/2):
            t=np.linspace(0,2*np.pi,60)
            wheels.append(np.stack([sx+wheel_r*np.cos(t), np.full_like(t,sy), wheel_r+wheel_r*np.sin(t)],axis=-1))
    return edges, wheels

def place(pts, origin, fwd, up):
    """Transform points from object frame to camera frame."""
    x=np.asarray(fwd,float); x=x/np.linalg.norm(x)
    z=np.asarray(up,float); z=z-x*(x@z); z/=np.linalg.norm(z)
    y=np.cross(z,x)
    M=np.column_stack([x,y,z])
    return (np.asarray(pts,float)@M.T)+np.asarray(origin,float)

# --- dimensionally accurate Model 3 / Model Y wireframe -----------------------
M3 = dict(name="Model 3 (Highland)", L=4.720, W=1.849, H=1.441, wb=2.875,
          track_f=1.580, track_r=1.580, front_overhang=0.845, wheel_r=0.334)
MY = dict(name="Model Y (Juniper)", L=4.792, W=1.921, H=1.624, wb=2.890,
          track_f=1.610, track_r=1.610, front_overhang=0.880, wheel_r=0.360)

def body_profile(spec):
    """Side silhouette (x from wheelbase midpoint, z from ground) and half-widths."""
    s = spec["H"]/1.441
    xf = spec["wb"]/2 + spec["front_overhang"]
    xr = xf - spec["L"]
    P = [(xf,0.30),(xf-0.03,0.72*s),(xf-0.55,0.86*s),(xf-1.00,1.02*s),
         (0.45,1.435*s),(0.05,spec["H"]),(-0.55,1.40*s),(-1.30,1.28*s),
         (-1.85,1.11*s),(xr+0.14,0.85*s),(xr,0.35),(xr,0.22),(xf,0.22)]
    return np.array(P,float), xf, xr

def car_wire(spec, n_cross=7):
    P, xf, xr = body_profile(spec)
    hw = spec["W"]/2
    lines=[]
    for y in (+hw*0.985, -hw*0.985):
        lines.append(np.column_stack([P[:,0], np.full(len(P),y), P[:,1]]))
    # greenhouse (narrower) outline
    G = np.array([p for p in P if p[1] > 1.0*spec["H"]/1.441],float)
    if len(G)>1:
        for y in (+hw*0.78, -hw*0.78):
            lines.append(np.column_stack([G[:,0], np.full(len(G),y), G[:,1]]))
    # cross sections
    for x in np.linspace(xr+0.15, xf-0.1, n_cross):
        z_top = np.interp(x, P[:,0][::-1], P[:,1][::-1])
        w = hw*(0.78 if z_top > 1.05*spec["H"]/1.441 else 0.985)
        lines.append(np.array([[x,-w,0.22],[x,-w,z_top],[x,w,z_top],[x,w,0.22]]))
    wheels=[]
    for sx,tr in ((spec["wb"]/2,spec["track_f"]),(-spec["wb"]/2,spec["track_r"])):
        for sy in (+tr/2,-tr/2):
            t=np.linspace(0,2*np.pi,60)
            wheels.append(np.stack([sx+spec["wheel_r"]*np.cos(t), np.full_like(t,sy),
                                    spec["wheel_r"]+spec["wheel_r"]*np.sin(t)],axis=-1))
    return lines, wheels
