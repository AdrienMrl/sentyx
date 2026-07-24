// teslcam-case.scad — enclosure for the in-car unit (EARLY DRAFT, dimensions invented)
//
// Contents: Raspberry Pi 4B + Waveshare SIM7600 HAT (stacked on GPIO),
// M.2 SSD in a USB enclosure (side bay), SSD1306 0.96" OLED (in lid).
//
// Render:  openscad -o base.stl -D 'part="base"' teslcam-case.scad
//          openscad -o lid.stl  -D 'part="lid"'  teslcam-case.scad
//
// Coordinate system: X = length (port end at +X), Y = width (Pi compartment
// at -Y, SSD bay at +Y), Z = up. Origin at outside bottom rear-left corner.

part = "assembly"; // "base" | "lid" | "assembly"
explode = 0;       // lift the lid in the assembly view (illustrations)

/* [Shell] */
wall = 2.4;        // side wall thickness
floor_t = 2.4;     // floor thickness
lid_t = 2.4;       // lid plate thickness
int_h = 32;        // interior height, floor top -> lid underside
corner_r = 4;      // outer corner radius
fit = 0.25;        // lid lip clearance per side

/* [Raspberry Pi 4B] */
pi_l = 85;
pi_w = 56;
pi_pcb_t = 1.6;
pi_hole_dx = 58;       // mounting hole pitch, length axis
pi_hole_dy = 49;       // mounting hole pitch, width axis
pi_hole_off = 3.5;     // hole center from board edges
pi_clear = 1.5;        // gap board edge -> wall
pi_front_gap = 1;      // board front edge -> inside of port wall
standoff_h = 5;        // floor -> board underside
standoff_d = 6.5;
standoff_pilot = 2.2;  // M2.5 self-tap pilot

/* [SSD bay — M.2 USB enclosure] */
ssd_l = 100;
ssd_w = 31;
ssd_h = 10;
ssd_clear = 1.5;
ssd_rear_free = 6;     // free space behind SSD (rear screw pillar lives here)

/* [Divider] */
div_t = 2;

/* [OLED — SSD1306 0.96"] */
oled_hole_pitch = 23.5;   // measure your module! they vary
oled_post_d = 5;
oled_post_h = 3;
oled_pilot = 1.6;         // M2 self-tap pilot
oled_win_w = 24;          // visible window
oled_win_h = 13;
oled_win_dy = -1.5;       // display glass is offset from PCB center; tune after test print
oled_cx = 35;             // window center, absolute X
// window center Y is computed: middle of the SSD bay

/* [Antennas] */
sma_hole_d = 6.5;         // SMA bulkhead pigtail
sma_n = 2;                // MAIN + GPS (add a 3rd for DIV if wanted)
sma_pitch = 18;
sma_z = 18;

/* [Lid retention] */
pillar_d = 7;
pillar_pilot = 2.8;       // M3 self-tap pilot
lip_depth = 7;
lip_t = 2;
snap_w = 8;
snap_h = 2.5;

/* [Vents] */
vent_w = 2;

/* [Hidden] */
$fn = 40;
eps = 0.01;
weld = 0.4; // overlap so unioned features fuse into one manifold volume

// ---- derived ----
int_pi_w = pi_w + 2 * pi_clear;                 // 59
int_ssd_w = ssd_w + 2 * ssd_clear;              // 34
int_l = ssd_l + ssd_rear_free;                  // 106 (Pi needs less; extra = rear dead zone)
int_w = int_pi_w + div_t + int_ssd_w;           // 95
ext_l = int_l + 2 * wall;
ext_w = int_w + 2 * wall;
base_h = floor_t + int_h;

pi_x0 = wall + int_l - pi_l - pi_front_gap;     // rear edge of Pi PCB
pi_y0 = wall + pi_clear;
pcb_top = floor_t + standoff_h + pi_pcb_t;
bay_y0 = wall + int_pi_w + div_t;               // inner rear wall of SSD bay
dead_zone_l = pi_x0 - wall;                     // free area behind the Pi

oled_cy = bay_y0 + int_ssd_w / 2;

// ---- primitives ----
module rounded_outline(inset = 0)
    offset(r = corner_r - inset) offset(-corner_r)
        square([ext_l, ext_w]);

module vslot(h = 14) // vertical vent slot, punched along Y
    cube([vent_w, wall + 2, h], center = true);

// ---- base ----
module base() {
    difference() {
        union() {
            // shell
            difference() {
                linear_extrude(base_h) rounded_outline();
                translate([0, 0, floor_t])
                    linear_extrude(base_h)
                        offset(-wall) rounded_outline();
            }
            // divider between Pi compartment and SSD bay
            translate([wall, wall + int_pi_w, floor_t - weld])
                cube([int_l, div_t, int_h - 0.4 + weld]);
            // Pi standoffs
            for (dx = [0, pi_hole_dx], dy = [0, pi_hole_dy])
                translate([pi_x0 + pi_hole_off + dx, pi_y0 + pi_hole_off + dy, floor_t - weld])
                    difference() {
                        cylinder(d = standoff_d, h = standoff_h + weld);
                        translate([0, 0, weld - eps])
                            cylinder(d = standoff_pilot, h = standoff_h + 2 * eps);
                    }
            // rear lid-screw pillars (front is held by snaps + lip)
            for (y = [wall + 3.2, ext_w - wall - 3.2])
                translate([wall + 3.2, y, floor_t - weld])
                    cylinder(d = pillar_d, h = int_h + weld);
        }

        // pillar pilot holes
        for (y = [wall + 3.2, ext_w - wall - 3.2])
            translate([wall + 3.2, y, base_h - 12])
                cylinder(d = pillar_pilot, h = 13);

        // port bay: Pi USB-A x2 + Ethernet, one open cutout in the +X wall
        translate([ext_l - wall - eps, pi_y0 + 1, floor_t + standoff_h - 0.5])
            cube([wall + 2 * eps, pi_w - 2, 19.5]);

        // SSD bay mouth in the +X wall (cable exit; SSD drops in from the top)
        translate([ext_l - wall - eps, bay_y0 + 2, floor_t + 1])
            cube([wall + 2 * eps, int_ssd_w - 4, 15]);

        // Pi side-connector cutouts in the -Y wall: USB-C, micro-HDMI x2
        // (positions from the Pi's rear edge; audio jack intentionally omitted)
        for (c = [[11.2, 10, 5.5], [26, 8.5, 6], [39.5, 8.5, 6]])
            translate([pi_x0 + c[0] - c[1] / 2, -eps, floor_t + standoff_h + 1])
                cube([c[1], wall + 2 * eps, c[2]]);

        // SMA bulkhead holes in the rear wall (pigtails live in the dead zone)
        for (i = [0 : sma_n - 1])
            translate([-eps, wall + int_pi_w / 2 - sma_pitch * (sma_n - 1) / 2 + i * sma_pitch, sma_z])
                rotate([0, 90, 0])
                    cylinder(d = sma_hole_d, h = wall + 2 * eps);

        // snap windows (bumps on the lid lip click into these)
        for (p = [[85, wall / 2], [30, ext_w - wall / 2], [85, ext_w - wall / 2]])
            translate([p[0], p[1], base_h - 4.5])
                cube([snap_w, wall + 2, snap_h], center = true);

        // side vents
        for (x = [68 : 6 : 80])   // -Y wall, aft of the HDMI cutouts
            translate([x, wall / 2, 20]) vslot(10);
        for (x = [55 : 6 : 97])   // +Y wall, over the SSD bay
            translate([x, ext_w - wall / 2, 16]) vslot(14);
        for (y = [70 : 6 : 94])   // rear wall, SSD side
            translate([wall / 2, y, 16]) rotate([0, 0, 90]) vslot(14);

        // floor vents under the Pi
        for (x = [pi_x0 + 10 : 8 : pi_x0 + 75])
            translate([x, pi_y0 + pi_w / 2, floor_t / 2])
                cube([vent_w, 36, floor_t + 2], center = true);

        // zip-tie slots for the SSD (two straps)
        for (x = [30, 70], y = [bay_y0 + 3, bay_y0 + int_ssd_w - 3])
            translate([x, y, floor_t / 2])
                cube([4, 2, floor_t + 2], center = true);
    }
}

// ---- lid ----
module lid() {
    difference() {
        union() {
            // plate
            linear_extrude(lid_t) rounded_outline();
            // inner lip
            translate([0, 0, lid_t - weld])
                difference() {
                    linear_extrude(lip_depth + weld)
                        offset(-wall - fit) rounded_outline();
                    translate([0, 0, -eps])
                        linear_extrude(lip_depth + weld + 2 * eps)
                            offset(-wall - fit - lip_t) rounded_outline();
                    // clear the rear screw pillars
                    for (y = [wall + 3.2, ext_w - wall - 3.2])
                        translate([wall + 3.2, y, -eps])
                            cylinder(d = pillar_d + 2, h = lip_depth + weld + 2 * eps);
                    // clear the divider top
                    translate([wall, wall + int_pi_w - 0.5, -eps])
                        cube([int_l, div_t + 1, lip_depth + weld + 2 * eps]);
                }
            // snap bumps, mirroring the base windows
            for (p = [[85, wall + fit + lip_t / 2, 0], [30, ext_w - wall - fit - lip_t / 2, 1],
                      [85, ext_w - wall - fit - lip_t / 2, 1]])
                translate([p[0], p[1] + (p[2] == 0 ? -lip_t / 2 : lip_t / 2), lid_t + lip_depth - 3])
                    rotate([0, 90, 0])
                        cylinder(r = 0.7, h = snap_w - 2, center = true, $fn = 16);
            // OLED posts (M2 self-tap)
            for (dx = [-1, 1], dy = [-1, 1])
                translate([oled_cx + dx * oled_hole_pitch / 2,
                           oled_cy + dy * oled_hole_pitch / 2, lid_t - weld])
                    difference() {
                        cylinder(d = oled_post_d, h = oled_post_h + weld);
                        cylinder(d = oled_pilot, h = oled_post_h + weld + 1);
                    }
        }

        // OLED window, chamfered outward
        translate([oled_cx, oled_cy + oled_win_dy, -eps]) {
            linear_extrude(lid_t + 2 * eps)
                square([oled_win_w, oled_win_h], center = true);
            hull() {
                linear_extrude(eps) square([oled_win_w + 3, oled_win_h + 3], center = true);
                translate([0, 0, lid_t]) linear_extrude(eps)
                    square([oled_win_w, oled_win_h], center = true);
            }
        }

        // rear M3 screw holes, countersunk
        for (y = [wall + 3.2, ext_w - wall - 3.2])
            translate([wall + 3.2, y, 0]) {
                translate([0, 0, -eps]) cylinder(d = 3.4, h = lid_t + 2 * eps);
                translate([0, 0, -eps]) cylinder(d1 = 6.4, d2 = 3.4, h = 1.7);
            }

        // label — engraved on the OUTSIDE face (z=0 as printed; the lid flips
        // onto the case), mirrored so it reads correctly once installed
        translate([ext_l / 2, wall + int_pi_w / 2, -0.1])
            linear_extrude(0.7)
                mirror([1, 0])
                    text("teslcam", size = 8, halign = "center", valign = "center",
                         font = "Helvetica:style=Bold");
    }
}

// ---- output ----
if (part == "base") base();
if (part == "lid") lid();
if (part == "assembly") {
    color("SteelBlue") base();
    color("LightSteelBlue", 0.6)
        translate([0, 0, base_h + lid_t + 0.2 + explode])
            mirror([0, 0, 1]) lid();
    // ghost hardware
    color("Crimson", 0.5) translate([pi_x0, pi_y0, floor_t + standoff_h])
        cube([pi_l, pi_w, pi_pcb_t]);                                  // Pi
    color("DarkGreen", 0.5) translate([pi_x0, pi_y0, floor_t + standoff_h + 12.6])
        cube([65, pi_w, pi_pcb_t]);                                    // HAT
    color("Silver", 0.5) translate([wall + ssd_rear_free, bay_y0 + ssd_clear, floor_t])
        cube([ssd_l, ssd_w, ssd_h]);                                   // SSD enclosure
}
