# Hardware component audit — 2026-09-14

## Verdict

The repository has a useful proposed architecture, but no reconciled, procurement-ready or dimensionally verified BOM. Do not treat the existing case as a fit-verified starting point. This audit covers repository evidence and targeted manufacturer documentation; it does not establish physical inventory or live Pi configuration. No hardware was ordered or tested.

## Conflicting baselines

- `docs/hardware-plan.md` (July 13): Pi 4B, endurance microSD, SIM7670G-class USB modem; power hardware undecided.
- `hardware/lte-dongle.md`: documented working prototype uses ASR USB ID `2ecc:3012`, RNDIS, Hologram; includes cold-boot recovery and restricted LTE routing.
- `docs/production-bom.md` (July 26, explicitly proposed): Pi 4B 1GB, EC25-AFX with USB carrier, 256GB NVMe with bare USB bridge, supercaps, ST7789 LCD, two buttons.
- `hardware/case/ASSEMBLY.md` and `teslcam-case.scad`: SIM7600 HAT, cased SSD, SSD1306 OLED, MAIN + GPS bulkheads, self-tapping lid screws. Both explicitly describe invented dimensions. These differ from the production proposal's modem, display, antennas, storage packaging and fasteners; the case also lacks the proposed power hardware.

## Component register

| Component | Evidence/status | Needed before enclosure dimensions can be frozen |
|---|---|---|
| Pi 4 Model B | Board family field-proven according to project records; 1GB proposed | Exact board revision/inventory and connector/underside keep-outs |
| LTE | ASR dongle documented deployed; EC25-AFX proposed replacement | Freeze actual modem and firmware, exact carrier SKU, SIM access, regulator and connector envelopes |
| Storage | microSD in older plan; 256GB NVMe in newer proposal | Exact SSD SKU/form factor, endurance and thermal specs; capacity/retention decision |
| USB–NVMe bridge | Generic bare board proposed | Exact board, chipset/firmware, mounting, connector direction, boot/UAS and power tests |
| Power buffer | Two 100F cells plus generic UPS board proposed | Full schematic and fixed SKUs, charge current limiting, balancing, isolation, converter limits, power-fail signal and shutdown behavior |
| Display | 1.3-inch ST7789 proposed; old case fits 0.96-inch OLED | Exact module drawing, glass/PCB envelope, window, hole pitch and pinout |
| Buttons | Two tactile switches proposed | Switch SKU, function, mounting, travel and GPIO allocation |
| LTE antennas | MAIN + receive diversity proposed; old case uses MAIN + GPS | Antenna/coax SKUs, supported bands, placement and cable bend/connector space; decide if GNSS is needed |
| Thermal parts | Generic heatsinks/pads proposed | Dimensions, retention and actual heat path; whole-unit hot-ambient qualification |
| Tesla cable | Generic right-angle USB-C cable proposed | Actual vehicle-side connector, both ends, plug orientation/envelope and power/data wiring |
| Internal harness | Generic jumpers/wire; old case has external USB loops | Complete cable/connector quantities, lengths, bend space, strain relief and retention |
| Enclosure hardware | ASA and M2.5 inserts proposed; old case uses mixed self-tappers | Exact inserts, screw lengths/counts, standoffs, lens and glovebox mounting method |
| Hologram SIM | Service used by documented prototype | SIM size/carrier compatibility and access; separate recurring cost from hardware |

## Significant findings

### 1. Power topology is incomplete, and the peak estimate is not conservative

The production BOM combines a 6W Pi, 2W SSD and a quoted 2A at 3.8V modem burst, then calls the peak 9–10W. Simultaneous stated loads instead total 15.6W before conversion losses, display and capacitor charging. This is an arithmetic scenario, not a measured load; the exact mini-PCIe carrier input requirements must replace the generic modem assumption.

The Pi 4 has a separate aggregate downstream USB limit of 1.2A: supplying a larger upstream buffer alone does not remove that limit for the SSD and modem. See [Raspberry Pi hardware documentation](https://www.raspberrypi.com/documentation/computers/raspberry-pi.html). The design needs both sustained and transient measurements, including discharged-cap startup. Specify VBUS backfeed prevention and how Tesla data remains connected while power is buffered. A capacitor cannot close a sustained energy deficit.

### 2. Supercapacitor runtime and heat claims are unqualified

Two 100F series cells give 50F. Stored energy at 5V is 625J, but usable energy is `0.5 * C * (Vhigh² - Vlow²)` multiplied by conversion efficiency. As an illustration only, 5V to 3V at 85% efficiency yields 340J, or 34 seconds at 10W; converter cutoff, ESR, aging and temperature can change this substantially. The BOM's 30–60 seconds is not validated.

The claim that a hot glovebox does not degrade EDLC cells is incorrect. Temperature and voltage affect lifetime, and an 85°C rating may require voltage derating for the selected part. See [Eaton application guide](https://www.eaton.com/us/en-us/products/electronic-components/topics/supercapacitor-applications-guide.html). Fixed cell/board specifications and measured shutdown margin are required.

### 3. Modem temperature claim needs correction for the exact variant

Quectel's currently indexed EC25 Mini PCIe table confirms EC25-AFX B71 support and lists 30 × 51 × 4.9mm, normal operation −35…+75°C and extended −40…+80°C. That conflicts with the proposal's −40…+85°C. The [manufacturer datasheet endpoint](https://www.quectel.com/blog/document/lte-ec25-mini-pcie-series/) returned 403 when directly opened during this audit; these figures are from its indexed table and should be checked against the supplied SKU's downloadable datasheet before selection. Carrier and antenna envelopes are additional to module dimensions.

### 4. Proposed peripherals are ahead of the implementation

`internal/blepair/bluez.go` registers `NoInputNoOutput`; its display callbacks are no-ops. The proposed displayed six-digit confirmation workflow is not implemented there. Targeted searches of `cmd/`, `internal/` and `scripts/` found no ST7789/SSD1306 driver or supercap/power-fail implementation under those terms. Treat display, button and shutdown integration as implementation work, not existing capabilities.

An EC25 carrier also needs integration testing against the current ASR-specific router API, watchdog and interface/routing assumptions. Preserve the LTE allowlist and policy routing while adapting modem management.

### 5. Cost and sourcing are planning estimates

The thirteen hardware prices sum to $138.70, consistent with the rounded $139 total. However, most entries identify a class of part or marketplace rather than manufacturer part numbers. The older plan requires controlled suppliers and fixed SKUs; the newer BOM's generic marketplace entries do not satisfy that gate. Shipping, harness completeness, assembly, qualification and current quotes are unresolved. Prices were not re-quoted in this audit.

## Next steps

1. Reconcile the intended next unit: production proposal versus enclosure for the physical prototype. Keep those as separately identified revisions.
2. Select and bench-test the power path, modem/carrier and SSD/bridge together; they determine space and thermals.
3. Freeze exact module, cable, switch, antenna and fastener SKUs. Record quantity, supplier, drawing/STEP source, measured dimensions, status (proposed/ordered/on hand/verified) and power/temperature limits.
4. Build simplified component solids from drawings and measurements, including plugged-in cables and installation paths. Only then finalize case mounting points and cutouts.

The production proposal is a reasonable candidate register. It is not yet a reliable shopping list or a final CAD assembly specification.
