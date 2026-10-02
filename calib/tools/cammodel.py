"""Fisheye (equidistant + polynomial) camera model, vectorised.

Works for every Tesla camera: with k1=1/3, k2=2/15 the equidistant polynomial
reproduces a pinhole exactly, so narrow and wide lenses share one model.
"""
import numpy as np

class Cam:
    def __init__(self, f, cx, cy, k=(0.,0.,0.,0.)):
        self.f=float(f); self.cx=float(cx); self.cy=float(cy); self.k=np.array(k,float)

    @property
    def params(self):
        return np.r_[self.f, self.cx, self.cy, self.k]

    @staticmethod
    def from_params(p):
        return Cam(p[0], p[1], p[2], p[3:7])

    def theta_d(self, th):
        k=self.k
        return th*(1 + k[0]*th**2 + k[1]*th**4 + k[2]*th**6 + k[3]*th**8)

    def project(self, V):
        """V: Nx3 camera-frame directions -> Nx2 pixels."""
        V=np.atleast_2d(np.asarray(V,float))
        r_xy=np.linalg.norm(V[:,:2],axis=1)
        th=np.arctan2(r_xy, V[:,2])
        sc=np.where(r_xy>1e-12, self.theta_d(th)/np.maximum(r_xy,1e-12), 0.0)
        return np.c_[self.f*sc*V[:,0]+self.cx, self.f*sc*V[:,1]+self.cy]

    def unproject(self, P, iters=12):
        """P: Nx2 pixels -> Nx3 unit rays in camera frame."""
        P=np.atleast_2d(np.asarray(P,float))
        x=(P[:,0]-self.cx)/self.f; y=(P[:,1]-self.cy)/self.f
        rd=np.hypot(x,y)
        th=rd.copy()
        for _ in range(iters):
            k=self.k
            f_=th*(1+k[0]*th**2+k[1]*th**4+k[2]*th**6+k[3]*th**8)-rd
            df=1+3*k[0]*th**2+5*k[1]*th**4+7*k[2]*th**6+9*k[3]*th**8
            th=th-f_/np.maximum(df,1e-9)
        th=np.clip(th,0,np.pi*0.999)
        s=np.where(rd>1e-12, np.sin(th)/np.maximum(rd,1e-12), 0.0)
        V=np.c_[s*x, s*y, np.cos(th)]
        return V/np.linalg.norm(V,axis=1,keepdims=True)

    def hfov_deg(self, w):
        th=self.solve_theta((w/2)/self.f)
        return 2*np.degrees(th)

    def solve_theta(self, rd, iters=30):
        th=rd
        for _ in range(iters):
            k=self.k
            f_=th*(1+k[0]*th**2+k[1]*th**4+k[2]*th**6+k[3]*th**8)-rd
            df=1+3*k[0]*th**2+5*k[1]*th**4+7*k[2]*th**6+9*k[3]*th**8
            th=th-f_/max(df,1e-9)
        return th

def rotvec_to_R(r):
    r=np.asarray(r,float); th=np.linalg.norm(r)
    if th<1e-12: return np.eye(3)
    a=r/th; K=np.array([[0,-a[2],a[1]],[a[2],0,-a[0]],[-a[1],a[0],0]])
    return np.eye(3)+np.sin(th)*K+(1-np.cos(th))*K@K

def R_to_rotvec(R):
    c=(np.trace(R)-1)/2; c=np.clip(c,-1,1); th=np.arccos(c)
    if th<1e-9: return np.zeros(3)
    v=np.array([R[2,1]-R[1,2],R[0,2]-R[2,0],R[1,0]-R[0,1]])/(2*np.sin(th))
    return v*th

def euler_from_R(R_cw):
    """Camera pose angles in the world frame (X fwd, Y left, Z up).

    Returns yaw, pitch, roll in degrees for the camera's optical axis,
    where yaw is measured from world +X toward +Y (left-positive).
    """
    # optical axis (camera +z) expressed in world
    z_w = R_cw.T @ np.array([0,0,1.0])
    x_w = R_cw.T @ np.array([1.0,0,0])
    yaw = np.degrees(np.arctan2(z_w[1], z_w[0]))
    pitch = np.degrees(np.arcsin(np.clip(z_w[2],-1,1)))     # + = looking up
    # roll: rotation of image x-axis about the optical axis
    up_ref = np.array([0,0,1.0]) - z_w*np.dot(z_w,[0,0,1.0])
    n=np.linalg.norm(up_ref)
    if n<1e-9: return yaw,pitch,0.0
    up_ref/=n
    right_ref = np.cross(z_w, up_ref)
    roll = np.degrees(np.arctan2(np.dot(x_w,up_ref), np.dot(x_w,right_ref)))
    return yaw, pitch, roll
