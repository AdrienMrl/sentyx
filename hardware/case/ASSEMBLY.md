# teslcam case — assembly (draft v0.1)

**Status: early draft.** Dimensions are plausible but invented — verify every
board against the printed part before trusting the fit, and expect a v0.2.
Everything is parametric in `teslcam-case.scad`; fix what's off and re-render.

## Print

| Part | File | Orientation | Settings |
|---|---|---|---|
| Base | `base.stl` | as modeled (floor on bed) | 0.2 mm layers, 3 walls, no supports |
| Lid | `lid.stl` | as modeled (flat face on bed, lip up) | same, no supports |

PETG or ASA recommended — the car interior gets hot; PLA will warp in a
parked car in summer. ~110 × 100 × 37 mm footprint, fits any common printer.

## Bill of materials

| Qty | Item | Used for |
|---|---|---|
| 1 | Raspberry Pi 4B | |
| 1 | Waveshare SIM7600 4G HAT + SIM | LTE uplink |
| 1 | M.2 SSD in USB 3 enclosure (≤100 × 31 × 10 mm) | clip storage |
| 1 | SSD1306 0.96" OLED, I2C | status display |
| 4 | M2.5 × 6 screws | Pi → case standoffs |
| 4 | M2.5 11 mm male–female standoffs + 4 nuts/screws | Pi → HAT stack |
| 2 | M3 × 8 self-tapping screws | lid, rear corners |
| 4 | M2 × 4 self-tapping screws | OLED → lid posts |
| 2 | SMA bulkhead pigtail (IPX/U.FL to SMA, ≥10 cm) | LTE MAIN + GPS antennas |
| 2 | Stick-on or SMA antennas | |
| 1 | USB-A → micro-B cable, ~15 cm | Pi USB 2 → HAT |
| 1 | USB-A → enclosure cable, ~15 cm | Pi USB 3 (blue) → SSD |
| 4 | F–F jumper wires | OLED I2C (3V3, GND, SDA, SCL) |
| 2 | Zip ties or a foam pad | SSD retention |

## Assembly order

1. **SIM first.** Insert the SIM into the HAT — the slot ends up hard to reach
   once the stack is in the case.
2. **SMA bulkheads.** Mount the two SMA pigtails through the rear-wall holes
   (nut outside). The slack lives in the dead zone behind the Pi.
3. **Pi in.** Set the Pi on the four standoffs, ports facing the big end
   opening, and fix with four M2.5 × 6 screws. The microSD stays reachable
   from inside the dead zone with the lid off.
4. **OLED wiring.** Connect the four jumper wires to the Pi's I2C pins
   (3V3 = pin 1, SDA = 3, SCL = 5, GND = 9) **before** seating the HAT — the
   HAT covers the header. Route them out toward the SSD bay over the divider.
   *(Open issue: if your HAT seats flush with no pass-through tails, use a
   stacking header or a GPIO splitter.)*
5. **HAT stack.** Screw the 11 mm standoffs onto the Pi's mounting holes,
   seat the HAT on the GPIO header, secure with the top screws. Connect the
   IPX ends of the pigtails to the HAT (MAIN + GPS).
6. **SSD.** Drop the enclosure into the side bay and strap it with zip ties
   through the floor slots (or a foam pad for friction fit). Its cable exits
   the bay mouth.
7. **Cables.** Both USB loops run outside the port wall and back in:
   HAT micro-B → Pi USB 2 port, SSD cable → Pi USB 3 (blue) port.
8. **OLED to lid.** Screw the OLED to the four lid posts (M2), display facing
   the window. Connect the jumpers.
9. **Lid on.** Front/side edges click into the three snap windows; two M3
   screws through the rear corners. Power via USB-C through its side cutout.

## Known gaps in v0.1 (by design, revisit in v0.2)

- SSD1306 mounting-hole pitch varies by vendor — `oled_hole_pitch` is set to
  23.5 mm; measure yours. Same for the window offset `oled_win_dy`.
- No cable strain relief at the external USB loop; consider a printed clip.
- The SIM7600 HAT's own SMA jacks may protrude past the HAT edge — if they
  foul the wall, raise `pi_clear` or notch the wall.
- Passive cooling only. If the Pi throttles in summer, v0.2 grows a 30 mm fan
  boss on the lid over the Pi compartment.
- No mounting ears/velcro base for fixing the case in the glovebox yet.
