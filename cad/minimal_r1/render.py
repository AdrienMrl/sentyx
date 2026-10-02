"""Offline orthographic previews from actual exported CAD triangulation, not AI images."""
import json
import math
from pathlib import Path
from PIL import Image, ImageDraw, ImageFont
import numpy as np

OUT=Path(__file__).resolve().parent/"output"
meshes=json.loads((OUT/"meshes.json").read_text())
TOP={"lid","display_bezel","lcd_retainer","button_carrier","button_1","button_2"}
def dot(a,b):return sum(x*y for x,y in zip(a,b))
def cross(a,b):return (a[1]*b[2]-a[2]*b[1],a[2]*b[0]-a[0]*b[2],a[0]*b[1]-a[1]*b[0])
def unit(v):
    l=math.sqrt(dot(v,v));return tuple(c/l for c in v) if l else (0,0,1)
eye=unit((-1,-1,1.15));right=unit((1,-1,0));up=cross(eye,right)
light=unit((-1,-2,4))
for mode in ("assembled","open","exploded","layout"):
    eye=unit((0,0,1)) if mode=="layout" else unit((-1,-1,1.15))
    right=(1,0,0) if mode=="layout" else unit((1,-1,0))
    up=cross(eye,right)
    polys=[];allpts=[]
    for mesh in meshes:
        name=mesh["name"]
        if mode=="assembled" and mesh["proxy"]:continue
        if mode=="open" and (name in TOP or name in {"lcd_board","button_pcb"}):continue
        if mode=="layout" and (name in TOP or name in {"lcd_board","button_pcb","ssd_shelf","ssd_end_stop","ssd_occupied"}):continue
        dz=65 if mode=="exploded" and (name in TOP or name in {"lcd_board","button_pcb"}) else 25 if mode=="exploded" and name in {"ssd_shelf","ssd_end_stop","ssd_occupied"} else 0
        vs=[(v[0],v[1],v[2]+dz) for v in mesh["vertices"]]
        rgb=tuple(int(mesh["color"].lstrip('#')[i:i+2],16) for i in (0,2,4))
        for face in mesh["faces"]:
            pts=[vs[i] for i in face]
            n=unit(cross(tuple(pts[1][i]-pts[0][i] for i in range(3)),tuple(pts[2][i]-pts[0][i] for i in range(3))))
            if dot(n,eye)<0:continue
            shade=.68+.32*max(0,dot(n,light))
            color=tuple(round(c*shade) for c in rgb)
            projected=[(dot(v,right),-dot(v,up)) for v in pts]
            allpts+=projected
            polys.append(([dot(v,eye) for v in pts],projected,color))
    im=Image.new('RGB',(1800,1400),'#f4f5f5');d=ImageDraw.Draw(im)
    mn=[min(v[i] for v in allpts) for i in (0,1)];mx=[max(v[i] for v in allpts) for i in (0,1)]
    scale=min(1580/(mx[0]-mn[0]),1120/(mx[1]-mn[1]))
    ox=(1800-scale*(mx[0]-mn[0]))/2;oy=170
    pixels=np.full((1400,1800,3),(244,245,245),dtype=np.uint8)
    depth=np.full((1400,1800),-np.inf)
    for zz,pts,color in polys:
        p=np.array([(ox+(x-mn[0])*scale,oy+(y-mn[1])*scale) for x,y in pts])
        x0=max(0,int(np.floor(p[:,0].min())));x1=min(1799,int(np.ceil(p[:,0].max())))
        y0=max(0,int(np.floor(p[:,1].min())));y1=min(1399,int(np.ceil(p[:,1].max())))
        if x1<x0 or y1<y0:continue
        a,b,c=p
        den=(b[1]-c[1])*(a[0]-c[0])+(c[0]-b[0])*(a[1]-c[1])
        if abs(den)<1e-8:continue
        yy,xx=np.mgrid[y0:y1+1,x0:x1+1];xx=xx+.5;yy=yy+.5
        u=((b[1]-c[1])*(xx-c[0])+(c[0]-b[0])*(yy-c[1]))/den
        v=((c[1]-a[1])*(xx-c[0])+(a[0]-c[0])*(yy-c[1]))/den
        w=1-u-v;z=u*zz[0]+v*zz[1]+w*zz[2]
        sub=depth[y0:y1+1,x0:x1+1]
        mask=(u>=-1e-7)&(v>=-1e-7)&(w>=-1e-7)&(z>sub)
        sub[mask]=z[mask];pixels[y0:y1+1,x0:x1+1][mask]=color
    im=Image.fromarray(pixels);d=ImageDraw.Draw(im)
    font=ImageFont.truetype('/System/Library/Fonts/Helvetica.ttc',32)
    small=ImageFont.truetype('/System/Library/Fonts/Helvetica.ttc',21)
    d.text((70,45),f'MINIMAL R1 / {mode.upper()}',font=font,fill='#263339')
    d.text((70,97),'220 x 190 x 84 mm shell | build123d geometry | prototype',font=small,fill='#53646b')
    d.text((70,1340),'Colored internal blocks are component reservations; hardware and cable routing require fit checks.',font=small,fill='#53646b')
    im.save(OUT/f'{mode}.png')
    print(OUT/f'{mode}.png')
