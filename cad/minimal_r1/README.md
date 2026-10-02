# Minimal R1 enclosure

Dimensioned prototype CAD inspired by concept 01. Source geometry is build123d;
generated images here are renders of that geometry, not image-generator mockups.

## Preview and files

Live OCP viewer: http://127.0.0.1:3939/viewer (server must be running).
Drag to orbit, scroll to zoom; eye icons toggle individual parts. `x` opens the
explode tool; clipping and measurement tools are in the toolbar.

From the repository root:

```sh
# Start viewer in one terminal, open the URL in a browser before sending geometry:
cad/.venv/bin/python -m ocp_vscode --host 127.0.0.1 --port 3939 --control orbit

# Rebuild, validate and send all named parts and reservations:
cad/.venv/bin/python cad/minimal_r1/enclosure.py --show

# Or load already-exported geometry quickly:
cad/.venv/bin/python cad/minimal_r1/preview.py assembly
cad/.venv/bin/python cad/minimal_r1/preview.py packaging
cad/.venv/bin/python cad/minimal_r1/preview.py exploded

# Offline CAD previews and export validation:
cad/.venv/bin/python cad/minimal_r1/render.py
cad/.venv/bin/python cad/minimal_r1/check_exports.py
```

`output/assembly.step` contains 20 separate printed parts at assembly positions.
`packaging.step` adds labelled component reservation blocks; these are NOT detailed
manufacturer models. `exploded.step` separates the upper assembly and SSD shelf.
Individual `.step` files stay in assembly coordinates. Individual `.stl` files
are oriented and translated onto Z=0 for printing. The two button, skid and antenna
strip files represent two instances each: print each named file once.

## Dimensions and layout

- Shell: **220 × 190 × 84mm**, including 6mm lid, 8mm corner radius.
- Printed assembly: 92mm overall height including 6mm lower skids and buttons.
  Cable and external screw heads add to the installed envelope.
- Walls 2.4mm; floor 3mm. Lid uses a 3mm-deep locating lip with 0.35mm lateral
  clearance, four M3 screws and inserts. Lid counterbores are 6.3mm diameter,
  3.2mm deep. This is a vented indoor/glovebox enclosure, not a sealed case.
- Bezel 91.4 × 55.4mm in a 92 × 56mm pocket; top face flush with lid.
  LCD aperture 24 × 24mm; buttons 8mm diameter in 8.6mm holes, 22mm pitch.
- Lower trays: Pi at front-left, modem at front-right, optional power at rear-left.
  Pi board origin (20,30), M2.5 holes at (23.5,33.5), (81.5,33.5),
  (23.5,82.5), (81.5,82.5): 58 × 49mm pitch.
- SSD shelf floor at Z=52mm, device at Z=55; power reservation ends at Z=50.
  There is 2mm nominal space below the shelf above the optional power envelope.
  Device cradle clear width 41mm, nominal reserved device length 125mm; two straps
  carry retention, and the replaceable end stop provides a coarse end boundary.
- Rear panel is split at the cable centreline so a connector need not pass through
  a small hole. Passage diameter 6.4mm, clamp bore 5.6mm. Use a compliant silicone
  liner and tune the bore for the actual cable jacket; never crush the cable.
- Left upper bay reserves **48 × 77 × 16mm** for SSD cable loops, separate from
  its 30mm plug allowance. Two rounded loops at an assumed 20mm centreline radius
  can consume roughly 360mm, leaving roughly 140mm of a 500mm cable for leads.
  These are packaging estimates; real plug shapes, stiffness and bend limits still
  require a routed fit check. The full wiring harness is not modelled.

The larger footprint/height replaces the earlier 190×150×65 estimate. It leaves
room for the complete SSD enclosure, replaceable adapters and optional UPS rather
than shrinking components to match the concept image. Fits a nominal 250mm build
plate with brim; a 220mm bed leaves no X margin and is not the recommended setup.
Check the actual glovebox space before printing the whole shell.

## Mounting and hardware

| Joint | Hardware / method |
|---|---|
| Lid → base | 4 M3×8 socket screws; 4 Ruthex RX-M3x5.7 inserts |
| SSD shelf → base | 4 M3×8 screws; 4 RX-M3x5.7 inserts |
| Three lower trays → base | 12 M3×10 screws with ~1mm washers; 12 M3 hex nuts in underside pockets |
| Pi → tray | 4 female-threaded M2.5×6 nylon spacers; M2.5×4 screws through 1.6mm PCB, M2.5×5 through 3mm tray. Check actual spacer thread depth. |
| Modem / UPS → adapters | 2.8mm grid holes provided; drill the removable plate to measured board holes if grid does not match. Use insulated spacers. Do not put a screw through a board merely to match the grid. |
| Bezel → lid | 4 M2.5×8 screws; 4 M2.5 nuts in underside hex pockets |
| LCD retainer / button carrier → bezel | 6 M2×8 thread-forming screws into 2mm pilot holes; pilot coupon and low torque required |
| Rear panel feet → ledges | 2 M3×10 screws and nuts, accessible before lid installation |
| Cable clamp halves | 2 M3×20 screws and nuts through vertical holes |
| Split rear panel → clamp halves | 4 M3×16 screws and nuts through horizontal holes. These are offset from vertical clamp fasteners. |
| SSD end stop → shelf | 1 M3×10 screw and nut, outside the device reservation |
| Two lower mounting skids | 4 M3×10 screws from inside floor, nuts in skid underside pockets; two 15mm straps pass through skid tunnels |
| SSD → shelf | Two 10mm silicone straps through slots; thin non-slip pads; keep thermal surface exposed |
| Antenna strips → shell | Removable high-temperature hook-and-loop pads in side gap; actual adhesive rating to be selected for measured temperature. Strips are light and do not carry PCBs. |

Eight heat-set inserts are installed in the print (4 lid, 4 shelf). Insert pilot
diameter starts at 4.0mm, depth 6.2mm. Print and measure a coupon with the actual
filament/printer/insert before installing inserts into the full enclosure.

LCD retention intentionally avoids inventing a vendor PCB hole pattern. The
rear frame clamps board edges using insulating compliant pads, with shim thickness
set from the actual module stack. Do not press the display glass or connector.
The model reserves a 45×31mm PCB and assumed 6mm upper stack; window offset and
retainer height are adjustable in the source. R1 leaves the screen exposed;
the earlier separate protective lens proposal is deferred to a replacement bezel
after measuring glass height. Buttons require the specified custom PCB; its switch
tops are nominally Z=72.9, beneath plungers at Z=73 (0.1mm initial gap).

## Print and assemble

Start with ASA, 0.2mm layers, 4 perimeter walls, 5–6 solid top/bottom layers,
and 20–30% infill; use an enclosure and your filament manufacturer's settings.
These are starting slicer settings, not an established qualified process.

1. Print small fit coupons/bezel/clamp first. Check inserts, lid fit, button travel,
   actual display and jacket diameter. Inspect slicer layer previews before printing.
2. Print base floor-down; lid and bezel exterior-face-down (already oriented in STL).
   Selective supports may be needed at base rear ledges, clamp bores and panel feet.
   Button STLs put the cap on the bed; check their small flange overhangs.
3. Install base inserts and captured nuts. Attach skids and the three lower trays.
   Install board spacers and electronics on trays before closing restricted access.
4. Fit optional power module only after electrical qualification; leave its bay empty
   for initial direct-power operation. Fit modem SIM before mounting its board.
5. Route cable coils in the reserved upper-left volume; install SSD shelf, cable and
   retention straps. Ensure screw heads and ties do not touch the SSD or power module.
6. Attach antennas to removable strips, route micro-coax without tension, and retain
   strips at the sides. Maintain the provisional separation from metal; RF testing
   determines final antenna placement.
7. Assemble split rear panel around the cable. Tighten horizontal panel screws before
   vertical clamp screws. Check that jacket restraint is effective without damage.
8. Fit LCD pads/retainer and button PCB/plungers to bezel, then bezel to lid. Check
   every button releases freely. Attach labelled removable signal harnesses and leave
   enough service loop to lift the lid. Close with four lid screws.

## Checks and limits

`validation.json`: each printable part is one valid solid; pairwise plastic
intersections, component-to-plastic and component-to-component intersections,
and conservative cable reservations are checked. Touching support surfaces are
allowed. `export-checks.json` records watertight STL edge checks, bed alignment and
STEP round-trip validity/solid count. These checks do not establish fastener loads,
assembly accessibility, exact purchased-board fit or manufacturing tolerances.

Hardware screws/spacers and flexible cables are specified but not fully modelled;
full travel/insertion paths are not swept. The modem/UPS adapters still need the
actual board-revision mounting pattern. Prototype assembly verification therefore
remains necessary. Thermal, RF, electrical supply and Tesla compatibility limits
from `../design-basis.md` still apply. No fleet/production readiness is implied.
