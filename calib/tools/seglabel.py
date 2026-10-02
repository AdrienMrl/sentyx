"""Detect line segments and render a numbered overlay for manual axis labelling."""
import cv2, numpy as np, sys, os, argparse
sys.path.insert(0,os.path.dirname(os.path.abspath(__file__)))
from manhattan import detect_segments

def main():
    a=argparse.ArgumentParser(); a.add_argument("img"); a.add_argument("stem")
    a.add_argument("--minlen",type=float,default=45); a.add_argument("--top",type=int,default=70)
    a.add_argument("--panels",type=int,default=1); a.add_argument("--target",type=int,default=1700)
    a.add_argument("--box",nargs=4,type=int,default=None)
    p=a.parse_args()
    img=cv2.imread(p.img); H,W=img.shape[:2]
    S=detect_segments(img,min_len=p.minlen)
    L=np.hypot(S[:,2]-S[:,0],S[:,3]-S[:,1])
    o=np.argsort(-L); S=S[o][:p.top]; L=L[o][:p.top]
    np.save(p.stem+"_segs.npy", S)
    box = p.box or [0,0,W,H]
    x0,y0,x1,y1 = box
    vis=(img*0.5).astype(np.uint8)
    for i,(s,l) in enumerate(zip(S,L)):
        c=(int((s[0]+s[2])/2), int((s[1]+s[3])/2))
        cv2.line(vis,(int(s[0]),int(s[1])),(int(s[2]),int(s[3])),(0,0,255),2)
        cv2.putText(vis,str(i),c,cv2.FONT_HERSHEY_SIMPLEX,0.55,(0,255,255),2)
    crop=vis[y0:y1,x0:x1]
    sc=p.target/max(crop.shape[1],crop.shape[0])
    cv2.imwrite(p.stem+"_lab.png", cv2.resize(crop,None,fx=sc,fy=sc))
    print(f"{len(S)} segments saved; lengths {L.min():.0f}..{L.max():.0f}")
main()
