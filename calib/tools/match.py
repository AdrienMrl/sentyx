"""SIFT match two frames, keep distant-background matches, report geometry."""
import cv2, numpy as np, sys, json

def feats(path, mask_top=None):
    img = cv2.imread(path); g = cv2.cvtColor(img, cv2.COLOR_BGR2GRAY)
    sift = cv2.SIFT_create(nfeatures=20000, contrastThreshold=0.02)
    m = None
    if mask_top is not None:
        m = np.zeros(g.shape, np.uint8); m[:int(g.shape[0]*mask_top)] = 255
    kp, de = sift.detectAndCompute(g, m)
    return img, kp, de

def match(d1, d2, ratio=0.8):
    bf = cv2.BFMatcher()
    mm = bf.knnMatch(d1, d2, k=2)
    return [a for a,b in mm if a.distance < ratio*b.distance]

if __name__=="__main__":
    p1,p2,out = sys.argv[1],sys.argv[2],sys.argv[3]
    top = float(sys.argv[4]) if len(sys.argv)>4 else 0.6
    i1,k1,d1 = feats(p1, top); i2,k2,d2 = feats(p2, top)
    good = match(d1,d2)
    print("kp",len(k1),len(k2),"raw good",len(good))
    if len(good) < 8: sys.exit(0)
    P1 = np.float32([k1[m.queryIdx].pt for m in good])
    P2 = np.float32([k2[m.trainIdx].pt for m in good])
    H, inl = cv2.findHomography(P1,P2,cv2.USAC_MAGSAC, 3.0, maxIters=20000, confidence=0.9999)
    inl = inl.ravel().astype(bool)
    print("homography inliers", int(inl.sum()), "/", len(good))
    np.save(out+"_p1.npy", P1[inl]); np.save(out+"_p2.npy", P2[inl])
    vis = cv2.drawMatches(i1,k1,i2,k2,[g for g,o in zip(good,inl) if o][:120],None,
                          flags=cv2.DrawMatchesFlags_NOT_DRAW_SINGLE_POINTS)
    sc = 2200/vis.shape[1]
    cv2.imwrite(out+".png", cv2.resize(vis,None,fx=sc,fy=sc))
