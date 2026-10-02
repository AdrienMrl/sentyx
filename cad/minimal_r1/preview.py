"""Send existing STEP artifacts to the local OCP viewer without rebuilding."""
import argparse
import json
from pathlib import Path
from build123d import import_step
from ocp_vscode import show, Camera

p=argparse.ArgumentParser()
p.add_argument("view",choices=["assembly","packaging","exploded"],nargs="?",default="assembly")
a=p.parse_args()
out=Path(__file__).resolve().parent/"output"
shape=import_step(out/f"{a.view}.step")
show(shape,port=3939,reset_camera=Camera.RESET)
