"""Extract painted-line centerline segments from a parking-lot frame (multi-scale top-hat)."""
import cv2, numpy as np, json, sys

def paint_mask(img, k=121, thr=12, smax=90):
    g = cv2.cvtColor(img, cv2.COLOR_BGR2GRAY)
    hsv = cv2.cvtColor(img, cv2.COLOR_BGR2HSV)
    g = cv2.GaussianBlur(g,(3,3),0)
    acc = np.zeros(g.shape, np.uint8)
    for kk in (41, 81, k):
        se = cv2.getStructuringElement(cv2.MORPH_ELLIPSE,(kk,kk))
        th = cv2.morphologyEx(g, cv2.MORPH_TOPHAT, se)
        acc = np.maximum(acc, th)
    m = ((acc > thr) & (hsv[...,1] < smax)).astype(np.uint8)*255
    m = cv2.morphologyEx(m, cv2.MORPH_OPEN, np.ones((3,3),np.uint8))
    m = cv2.morphologyEx(m, cv2.MORPH_CLOSE, np.ones((7,7),np.uint8))
    return m

def segments(mask, min_len=80, min_area=400, max_thick=90):
    n, lab, stats, cent = cv2.connectedComponentsWithStats(mask, 8)
    out=[]
    for i in range(1,n):
        if stats[i,cv2.CC_STAT_AREA] < min_area: continue
        ys,xs = np.where(lab==i)
        P = np.c_[xs,ys].astype(float); mu = P.mean(0); Q = P-mu
        U,Sv,Vt = np.linalg.svd(Q, full_matrices=False)
        d = Vt[0]; t = Q@d; w = Q@Vt[1]
        L = t.max()-t.min(); thick = 4*w.std()
        if L < min_len or thick > max_thick or L < 3.0*max(thick,1): continue
        pts=[]
        for tt in np.linspace(t.min()+3, t.max()-3, max(5,int(L/20))):
            sel = np.abs(t-tt) < 5
            if sel.sum() < 4: continue
            pts.append((mu + d*tt + Vt[1]*np.median(w[sel])).tolist())
        if len(pts) < 5: continue
        # straightness check on centerline points
        Pp=np.array(pts); mu2=Pp.mean(0); _,_,V2=np.linalg.svd(Pp-mu2, full_matrices=False)
        resid = np.abs((Pp-mu2)@V2[1])
        if resid.max() > 6: continue
        out.append(dict(area=int(stats[i,cv2.CC_STAT_AREA]), length=float(L),
                        thick=float(thick), a=(mu+d*t.min()).tolist(), b=(mu+d*t.max()).tolist(),
                        pts=Pp.tolist(), resid=float(resid.max())))
    out.sort(key=lambda s:-s['length'])
    for k,s in enumerate(out): s['id']=k
    return out

if __name__=="__main__":
    img = cv2.imread(sys.argv[1]); H,W = img.shape[:2]
    y0 = float(sys.argv[3]) if len(sys.argv)>3 else 0.0
    m = paint_mask(img); m[:int(H*y0)] = 0
    segs = segments(m)
    json.dump(dict(w=W,h=H,segs=segs), open(sys.argv[2]+'.json','w'))
    vis = (img*0.55).astype(np.uint8)
    for s in segs[:70]:
        a=np.int32(s['a']); b=np.int32(s['b'])
        cv2.line(vis,tuple(a),tuple(b),(0,0,255),3)
        c=((a+b)//2)
        cv2.putText(vis,str(s['id']),tuple(c),cv2.FONT_HERSHEY_SIMPLEX,1.1,(0,255,255),3)
    sc = 1700/W
    cv2.imwrite(sys.argv[2]+'.png', cv2.resize(vis,None,fx=sc,fy=sc))
    print(len(segs),"segments; top lengths:", [round(s['length']) for s in segs[:16]])
