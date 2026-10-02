"""Minimal R1 enclosure. Millimetres; component proxies are explicitly reservations.

Run from any directory: cad/.venv/bin/python cad/minimal_r1/enclosure.py
STEP preserves separate named solids. STL parts are oriented on the print bed.
"""
from __future__ import annotations

import argparse
import json
from itertools import combinations
from pathlib import Path

from build123d import (
    Align, Axis, Box, Color, Compound, Cylinder, Location, RectangleRounded,
    RegularPolygon, export_step, export_stl, extrude,
)

OUT = Path(__file__).resolve().parent / "output"
W, D, BASE_H, LID_T = 220.0, 190.0, 78.0, 6.0
WALL, FLOOR, RADIUS = 2.4, 3.0, 8.0
INSERT_D, INSERT_DEPTH, M3_CLEAR = 4.0, 6.2, 3.4
corners = [(7, 7), (W-7, 7), (7, D-7), (W-7, D-7)]
MIN = (Align.MIN, Align.MIN, Align.MIN)
parts, proxies, keepouts = {}, {}, {}
colors = {}


def box(x, y, z, a, b, c):
    return Box(a, b, c, align=MIN).translate((x, y, z))


def cyl(x, y, z, diameter, height):
    return Cylinder(diameter/2, height, align=(Align.CENTER, Align.CENTER, Align.MIN)).translate((x,y,z))


def rounded(x,y,z,a,b,c,r=3):
    return extrude(RectangleRounded(a,b,r), amount=c).translate((x+a/2,y+b/2,z))


def hole(shape,x,y,z,d,h):
    return shape - cyl(x,y,z,d,h)


def add(name, shape, color="#a9afb1", flip=False):
    shape.label=name
    shape.color=Color(color)
    parts[name]=(shape,flip)
    colors[name]=color
    return shape


def reserve(name,xyz,size,color):
    p=box(*xyz,*size)
    p.label=name
    p.color=Color(color)
    proxies[name]=p
    colors[name]=color


def tray(x,y,a,b,name,mounts,board_holes=None):
    p=rounded(x,y,6,a,b,3,3)
    for cx,cy in corners:
        p -= cyl(cx,cy,5,12.5,5)
    for hx,hy in mounts:
        p=hole(p,hx,hy,5,M3_CLEAR,5)
    if board_holes:
        # Through holes accept M2.5 screws, separate 6mm nylon spacers and nuts.
        for hx,hy in board_holes:
            p=hole(p,hx,hy,5,2.8,5)
    else:
        # Removable adapter can be drilled to actual revision; sparse mounting grid.
        for hx in range(int(x+12),int(x+a-8),10):
            for hy in range(int(y+12),int(y+b-8),10):
                p=hole(p,hx,hy,5,2.8,5)
    return add(name,p,"#454e53")


def build():
    base=rounded(0,0,0,W,D,BASE_H,RADIUS)-rounded(WALL,WALL,FLOOR,W-2*WALL,D-2*WALL,BASE_H,RADIUS-WALL)
    for x,y in corners:
        base += cyl(x,y,FLOOR-.2,11,BASE_H-FLOOR+.2)
        base=hole(base,x,y,BASE_H-INSERT_DEPTH,INSERT_D,INSERT_DEPTH+1)
    # Tray bosses top at 6mm: captured M3 nuts are installed from the underside.
    pi_mounts=[(15,22),(111,22),(15,96),(111,96)]
    lte_mounts=[(126,22),(207,22),(126,96),(207,96)]
    power_mounts=[(15,109),(99,109),(15,180),(99,180)]
    for x,y in pi_mounts+lte_mounts+power_mounts:
        base += cyl(x,y,2.8,9,3.2)
        base=hole(base,x,y,-1,M3_CLEAR,8)
        nut=extrude(RegularPolygon(3.3,6),amount=2.6).translate((x,y,-.1))
        base -= nut
    # SSD shelf support pillars are outside the reserved power/SSD volumes.
    shelf_mounts=[(109,123),(208,123),(109,175),(208,175)]
    for x,y in shelf_mounts:
        base += cyl(x,y,2.8,10,49.2)
        base=hole(base,x,y,52-INSERT_DEPTH,INSERT_D,INSERT_DEPTH+1)
    # Narrow vertical inlet slots are printed as short 2.5mm bridges.
    for y in range(28,87,7):
        base -= box(-1,y,10,WALL+2,2.5,12)
        base -= box(W-WALL-1,y,10,WALL+2,2.5,12)
    # Rear panel opening runs to rim: USB plugs can pass before panel installation.
    base -= box(121,D-5,53,80,8,BASE_H-53+1)
    # Panel screw supports under rear ledge, away from shelf.
    for x in (127,195):
        base += box(x-5,D-8,47,10,5.6,6)
        base=hole(base,x,D-5.2,46,M3_CLEAR,9)
    # Strap tunnels through solid external-floor bosses (no intrusion into PCB trays).
    # Two removable mounting skids, separate parts, attach through base floor.
    for y in (14,183):
        for x in (60,160):
            base=hole(base,x,y,-1,M3_CLEAR,5)
    add("base",base)

    lid=rounded(0,0,BASE_H,W,D,LID_T,RADIUS)
    # Locating lip: 0.35mm clearance to shell; avoid posts and rear opening.
    lip=rounded(2.75,2.75,BASE_H-3,W-5.5,D-5.5,3.2,5.25)-rounded(4.25,4.25,BASE_H-4,W-8.5,D-8.5,5,3.75)
    for x,y in corners:
        lip -= cyl(x,y,BASE_H-4,13,6)
    lip -= box(119,D-7,BASE_H-4,84,9,6)
    lid += lip
    for x,y in corners:
        lid=hole(lid,x,y,BASE_H-1,M3_CLEAR,9)
        lid=hole(lid,x,y,BASE_H+LID_T-3.2,6.3,4)
    # Top vents: 3mm slots, 4mm ribs, split to keep each rib short/stiff.
    for y in range(104,170,7):
        for x in (25,114):
            lid -= rounded(x,y,BASE_H-1,80,3,9,1.4)
    # Recessed interchangeable control bezel, supported by a solid ledge.
    lid -= rounded(22,20,81.6,92,56,4,4)
    lid -= rounded(24,30,77,82,36,8,2)
    bezel_screws=[(27,25),(109,25),(27,71),(109,71)]
    for x,y in bezel_screws:
        lid=hole(lid,x,y,77,2.8,8)
        lid -= extrude(RegularPolygon(2.95,6),amount=2.3).translate((x,y,77.9))
    add("lid",lid,flip=True)

    bezel=rounded(22.3,20.3,81.6,91.4,55.4,2.4,3.7)
    bezel -= box(40,35,80,24,24,6)
    for x,y in bezel_screws:
        bezel=hole(bezel,x,y,81,2.8,4)
    for y in (37,59):
        bezel=hole(bezel,91,y,81,8.6,4)
    # LCD rear clamp mounting pillars. No invented LCD PCB hole pattern.
    for x,y in [(27,34),(80,34),(27,61),(80,61)]:
        bezel += cyl(x,y,71.6,6,10.2)
        bezel=hole(bezel,x,y,71.5,2.0,9)  # M2 self-tapping prototype clamp screws
    for y in (34,62):
        bezel += cyl(103,y,67,5,14.8)
        bezel=hole(bezel,103,y,66.9,2.0,11)
    add("display_bezel",bezel,"#353e44",flip=True)
    # Removable LCD retaining frame, with adjustable compliant foam pads at PCB edges.
    frame=rounded(23,30,69.2,61,35,2.4,2)-box(35,35,68,36,25,5)
    for x,y in [(27,34),(80,34),(27,61),(80,61)]:
        frame=hole(frame,x,y,68,2.3,5)
    add("lcd_retainer",frame,"#454e53")
    # Button carrier is a printable ledge for a fabricated 15x35 PCB, not electronics.
    carrier=rounded(80,26,64.6,25,44,2.4,2)
    for y in (34,62):
        carrier=hole(carrier,103,y,64,2.3,4)
    for y in (35,59):
        carrier -= box(81,y,64,2,4,4)  # PCB restraint tie slots
        carrier -= box(98,y,64,2,4,4)
    add("button_carrier",carrier,"#454e53")
    for i,y in enumerate((37,59),1):
        # Captive flange below bezel. Stem meets 4.3mm switch on 1.6mm PCB.
        button=cyl(91,y,79.8,11,1.5)+cyl(91,y,81.2,8,4.8)+cyl(91,y,73,3,7)
        add(f"button_{i}",button,"#353e44",flip=True)

    tray(11,18,104,82,"pi_tray",pi_mounts,[(23.5,33.5),(81.5,33.5),(23.5,82.5),(81.5,82.5)])
    tray(122,18,89,82,"lte_adapter",lte_mounts)
    tray(11,105,92,79,"power_adapter",power_mounts)
    shelf=rounded(74,119,52,139,61,3,3)
    for x,y in corners:
        shelf -= cyl(x,y,51,12.5,5)
    for x,y in shelf_mounts:
        shelf=hole(shelf,x,y,51,M3_CLEAR,5)
    # Side rails leave 41mm cradle width. Open ends accept adjustable end stop.
    shelf += box(77,127,54.8,128,2,5.2)+box(77,170,54.8,128,2,5.2)
    for x in (98,179):
        shelf -= box(x,124,51,11,2.5,5)
        shelf -= box(x,173,51,11,2.5,5)
    shelf -= rounded(207,145,51,5,3.4,5,1.6)
    add("ssd_shelf",shelf,"#454e53")
    stop=box(206,141,49,9,12,3)+box(213,141,51.8,2,12,13.2)
    stop=hole(stop,210,146.7,48,3.4,5)
    add("ssd_end_stop",stop,"#454e53")

    # Rear closure: flat print, open semicircular cable passage at the top.
    panel=box(121.35,D-2.4,53.35,79.3,2.4,24.3)
    # cylinder axis along Y: Z-axis cylinder rotated about X.
    bore=Cylinder(3.2,7).rotate(Axis.X,90).translate((161,D-1,70))
    panel -= bore
    # Horizontal foot sits on rear supports; M3 screws vertically into captive nuts.
    for x in (127,195):
        panel += box(x-4,D-7.4,53.35,8,5.2,3)
        panel=hole(panel,x,D-5.2,52,3.4,6)
    cap=panel.intersect(box(120,180,70,82,12,10))[0]
    panel -= box(120,180,70,82,12,10)
    add("rear_cable_panel",panel,"#353e44")
    add("rear_cable_cap",cap,"#353e44")
    # Separate split cable clamp on internal rear panel: two halves + screws/nuts.
    for n,z in [("lower",62),("upper",70)]:
        clamp=box(146,179,z,30,8.6,8)
        clamp -= Cylinder(2.8,12).rotate(Axis.X,90).translate((161,183,70))
        for x in (150,172):
            clamp=hole(clamp,x,183,z-1,3.4,10)
        add(f"cable_clamp_{n}",clamp,"#353e44")
    # Clamp attaches to panel via its lower half and two horizontal screws.
    # Through holes continue across panel; captured nuts are installed inside clamp.
    for name in ("rear_cable_panel","cable_clamp_lower","rear_cable_cap","cable_clamp_upper"):
        shape,flip=parts[name]
        z=65 if name in ("rear_cable_panel","cable_clamp_lower") else 74
        for x in (155,167):
            shape -= Cylinder(1.7,20).rotate(Axis.X,90).translate((x,185,z))
        shape.label=name;shape.color=Color(colors[name]);parts[name]=(shape,flip)

    for i,y in enumerate((14,183),1):
        skid=rounded(40,y-6,-6,140,12,6,3)
        for x in (60,160):
            skid=hole(skid,x,y,-7,3.4,9)
            skid -= extrude(RegularPolygon(3.3,6),amount=2.6).translate((x,y,-6.1))
        for x in (80,124):
            skid -= box(x,y-7,-3.5,16,14,2)
        add(f"mounting_skid_{i}",skid,"#353e44")
    # Antennas on plastic side strips in upper region; adhesive strips removable.
    for i,x in enumerate((5,213),1):
        strip=box(x,32,45,2,102,28)
        add(f"antenna_strip_{i}",strip,"#454e53")
        # Simple clip shoes lock onto wall top; slit fits wall with 0.35 each side.
        # Antenna strips are retained with removable high-temp hook/loop in R1.

    reserve("pi_occupied",(20,30,15),(95,56,30),"#539879")
    reserve("lte_occupied",(134,30,15),(65,57,30),"#4b87af")
    reserve("power_optional",(19,112,10),(80,70,40),"#c49458")
    reserve("ssd_occupied",(80,129,55),(125,40,18),"#77818b")
    # Conservative LCD module excluding deliberately modelled screw/clamp lands.
    reserve("lcd_board",(31,32,74),(45,31,6),"#539879")
    reserve("button_pcb",(84,31,67),(15,35,1.6),"#539879")
    for name,xyz,size in [
        ("pi_usb_plugs",(115,32,17),(18,52,22)),
        ("ssd_usb_plug",(49,140,58),(30,18,12)),
        ("front_wiring",(22,9,17),(181,8,28)),
        ("middle_wiring",(18,101,17),(187,9,28)),
        ("ssd_cable_loop",(23,93,55),(48,77,16)),
    ]:
        keepouts[name]=box(*xyz,*size);keepouts[name].label=name


def overlap(a,b):
    aa,bb=a.bounding_box(),b.bounding_box()
    if any(tuple(aa.max)[i] <= tuple(bb.min)[i]+1e-5 or tuple(bb.max)[i] <= tuple(aa.min)[i]+1e-5 for i in range(3)):
        return 0.0
    common=a.intersect(b)
    if common is None: return 0.0
    return abs(common.volume) if hasattr(common,"volume") else sum(abs(s.volume) for s in common)


def validate():
    result={"dimensions_mm":{"shell":[W,D,BASE_H+LID_T],"with_skids_and_buttons":[W,D,92]},"parts":{},"interferences":[],"proxy_interferences":[],"keepout_interferences":[]}
    for name,(p,_) in parts.items():
        result["parts"][name]={"valid":p.is_valid,"solids":len(p.solids()),"volume_mm3":round(p.volume,2),"bounds_mm":list(p.bounding_box().size)}
        if not p.is_valid or len(p.solids()) != 1: raise ValueError(f"Invalid printable part {name}: {result['parts'][name]}")
    for (na,(a,_)),(nb,(b,_)) in combinations(parts.items(),2):
        v=overlap(a,b)
        if v>.01:result["interferences"].append([na,nb,round(v,3)])
    for name,p in proxies.items():
        for n,(s,_) in parts.items():
            v=overlap(p,s)
            if v>.01:result["proxy_interferences"].append([name,n,round(v,3)])
    for (na,a),(nb,b) in combinations(proxies.items(),2):
        v=overlap(a,b)
        if v>.01:result["proxy_interferences"].append([na,nb,round(v,3)])
    for name,p in keepouts.items():
        for n,s in list(proxies.items())+[(n,s) for n,(s,_) in parts.items()]:
            v=overlap(p,s)
            if v>.01:result["keepout_interferences"].append([name,n,round(v,3)])
    return result


def main():
    parser=argparse.ArgumentParser();parser.add_argument("--show",action="store_true");args=parser.parse_args()
    OUT.mkdir(parents=True,exist_ok=True)
    build();report=validate()
    (OUT/"validation.json").write_text(json.dumps(report,indent=2)+"\n")
    print(json.dumps({k:v for k,v in report.items() if k!="parts"},indent=2),flush=True)
    if any(report[k] for k in ("interferences","proxy_interferences","keepout_interferences")):
        raise ValueError("Resolve reported collisions before exporting")
    for name,(part,flip) in parts.items():
        p=part.rotate(Axis.X,180) if flip else part
        # Rear panel and antenna strips print on their broad face.
        if name.startswith("rear_cable_") or name.startswith("antenna_strip"):
            p=part.rotate(Axis.X,90) if name.startswith("rear_cable_") else part.rotate(Axis.Y,90)
        mn=p.bounding_box().min
        p=p.translate((-mn.X,-mn.Y,-mn.Z))
        export_stl(p,OUT/f"{name}.stl",tolerance=.05,angular_tolerance=.15)
        export_step(part,OUT/f"{name}.step")
    assembly=Compound(label="Minimal_R1_printed_parts",children=[p for p,_ in parts.values()])
    export_step(assembly,OUT/"assembly.step")
    exploded=[]
    for n,(p,_) in parts.items():
        dz=65 if n in ("lid","display_bezel","lcd_retainer","button_carrier","button_1","button_2") else 25 if n in ("ssd_shelf","ssd_end_stop") else 0
        q=p.translate((0,0,dz));q.label=n;q.color=Color(colors[n]);exploded.append(q)
    export_step(Compound(label="Exploded_view_NOT_installation_positions",children=exploded),OUT/"exploded.step")
    export_step(Compound(label="Packaging_reference_NOT_hardware_CAD",children=[p for p,_ in parts.values()]+list(proxies.values())),OUT/"packaging.step")
    # Save triangulated actual CAD for a local standalone viewer and software previews.
    meshes=[]
    for name,p in [(n,p) for n,(p,_) in parts.items()]+list(proxies.items()):
        vertices,faces=p.tessellate(.15,.25)
        meshes.append({"name":name,"color":colors[name],"proxy":name in proxies,"vertices":[list(v) for v in vertices],"faces":faces})
    (OUT/"meshes.json").write_text(json.dumps(meshes,separators=(",",":")))
    if args.show:
        from ocp_vscode import show, Camera
        show(*[p for p,_ in parts.values()],*proxies.values(),names=list(parts)+list(proxies),reset_camera=Camera.KEEP)
    print(f"Exported {len(parts)} printable parts to {OUT}")


if __name__=="__main__":main()
