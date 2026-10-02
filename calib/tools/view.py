"""Render an image region with a labeled pixel grid, for reading off coordinates."""
import cv2, numpy as np, sys, argparse
def gridded(img, x0,y0,x1,y1, out, step=100, target=1500):
    crop = img[y0:y1, x0:x1].copy()
    h,w = crop.shape[:2]; sc = target/max(w,h)
    crop = cv2.resize(crop, None, fx=sc, fy=sc, interpolation=cv2.INTER_AREA)
    H,W = crop.shape[:2]
    ov = crop.copy()
    for X in range(int(np.ceil(x0/step))*step, x1, step):
        u = int((X-x0)*sc); cv2.line(ov,(u,0),(u,H),(0,255,255),1)
        cv2.putText(ov,str(X),(u+3,16),cv2.FONT_HERSHEY_SIMPLEX,0.45,(0,255,255),1)
    for Y in range(int(np.ceil(y0/step))*step, y1, step):
        v = int((Y-y0)*sc); cv2.line(ov,(0,v),(W,v),(255,255,0),1)
        cv2.putText(ov,str(Y),(3,v-4),cv2.FONT_HERSHEY_SIMPLEX,0.45,(255,255,0),1)
    cv2.addWeighted(ov,0.55,crop,0.45,0,crop)
    cv2.imwrite(out, crop); print(out, "region",x0,y0,x1,y1,"scale",round(sc,3))
if __name__=="__main__":
    a=argparse.ArgumentParser(); a.add_argument("img"); a.add_argument("out")
    a.add_argument("--box",nargs=4,type=int,default=None); a.add_argument("--step",type=int,default=100)
    a.add_argument("--target",type=int,default=1500)
    p=a.parse_args(); im=cv2.imread(p.img); H,W=im.shape[:2]
    b=p.box or [0,0,W,H]
    gridded(im,max(0,b[0]),max(0,b[1]),min(W,b[2]),min(H,b[3]),p.out,p.step,p.target)
