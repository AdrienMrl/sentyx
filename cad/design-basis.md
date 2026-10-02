# Enclosure R1 — selected design basis

Date: 2026-09-14. User authorized component selection and reasonable dimensional assumptions. This is the working baseline for the next enclosure, superseding the older case's component assumptions. It is a prototype selection, not a claim of vehicle qualification or an order. Historical BOMs remain records of earlier proposals.

Implementation update: [Minimal R1](minimal_r1/README.md) now supplies the CAD.
Its 220×190×84mm shell supersedes the initial 190×150×65mm estimate below.
It uses M2 prototype screws for the replaceable LCD retainer, split cable panels,
and an exposed LCD for the first fit test; the protective lens is deferred until
the exact glass stack is measured. See that assembly guide for installed hardware.

## Decisions

- Keep Pi 4B and USB gadget architecture. Reuse the existing Pi for the first build; specify Pi 4B 2GB for new purchases to avoid depending on the proposed industrial 1GB sourcing arrangement. No Pi 5/CM4 migration.
- Keep EC25-AFX, Hologram, 256GB SSD and ST7789 direction. Use identifiable modules with published dimensions.
- Use an industrial-temperature SSD and a complete, removable USB enclosure for R1. It costs space but avoids an unidentified bare bridge. The bridge/carrier/display remain temperature qualification limits.
- Passive ventilation, ASA shell, no lithium battery, no fan in R1. The passive thermal system must be tested; a heatsink cannot cool a device below ambient.
- Use a replaceable power cassette. Select AQEX qUPS-P-SC-1.3 for **Pi/SSD shutdown evaluation only**, not as a qualified supply for the complete load. R1 initially operates with the buffer bypassed. CAD accommodates it now; field electrical enablement follows integration tests.
- Target outer size **190 × 150 × 65mm** as a first packaging allowance, not a fit claim. Dimensions may grow after actual cable routing. There is no measured glovebox envelope yet.

## Selected component list

Dimensions are millimetres. “Reserve” dimensions are deliberate CAD allowances, not manufacturer dimensions. No listed component is assumed purchased.

| Qty | Item / selected part | Geometry and mounting decision | Evidence |
|---:|---|---|---|
| 1 | Raspberry Pi 4 Model B, existing board; 2GB for new purchase | 85 × 56 PCB; reserve 95 × 70 × 30 for board/connectors/heatsink before cable keep-outs. M2.5 board screws on removable tray. | [Pi product](https://www.raspberrypi.com/products/raspberry-pi-4-model-b/) |
| 1 | Quectel **EC25-AFX Mini PCIe** | 30 × 51 × 4.9 module; carried on S121, not independently mounted. Keep variant suffix. | [Quectel hardware design](https://quectel.com/content/uploads/2024/02/Quectel_EC25_Mini_PCIe_Hardware_Design_V2.4-2.pdf) |
| 1 | Sixfab **3G/4G & LTE Base HAT S121** | Published 65 × 57 PCB. Reserve 75 × 70 × 30 including modem/coax. Mount beside Pi on its own tray; USB data and a separately engineered power connection. Do not stack above heatsink. | [Technical details](https://docs.sixfab.com/docs/raspberry-pi-3g-4g-lte-base-hat-technical-details), [drawing/STEP reference](https://sixfab.com/wp-content/uploads/2020/03/Sixfab_3G-4G-LTE_Base_HAT_Datasheet_V1.0.pdf) |
| 1 | Transcend **TS256GMTE452T-I**, 256GB industrial NVMe | 2242, double-sided, 42 × 22 × 3.58. Install in EC-SNVE with clearance below both sides. Specified wide-temperature variant; no generic substitute inferred. | [Manufacturer](https://us.transcend-info.com/embedded/product/embedded-ssd-solutions/mte452t-mte452t-i) |
| 1 | Sabrent **EC-SNVE** USB enclosure | Published 4.59 × 1.24 × 0.5in ≈116.6 × 31.5 × 12.7mm. Reserve 125 × 40 × 18 plus 30mm at USB-C end. Removable strap cradle; retain supplied thermal interface. | [Manufacturer](https://sabrent.com/products/EC-SNVE) |
| 1 | Waveshare **1.3inch LCD Module**, ST7789, 240×240 (not LCD HAT) | Published 45 × 31 PCB, 23.4 × 23.4 active area. Reserve 50 × 38 × 15 behind removable bezel. Board thickness/connector/hole offsets remain drawing/measurement inputs. | [Wiki](https://www.waveshare.com/wiki/1.3inch_LCD_Module) |
| 2 | Omron **B3F-1000** tactile switch | Separate small FR4 button board on bezel, printed captive plungers. Reserve 35 × 15 × 12 for complete button assembly; do not hang switches from loose wire. Wake/status and pairing-confirm functions. | [Datasheet](https://components.omron.com/us-en/system/files/2023-01/datasheet_pdf/A070-E1.pdf) |
| 2 | Taoglas **FXUB63.07.0150C** flex LTE antenna | Current product body: 96 × 21 × 0.2, 150mm coax, U.FL-compatible connector, 600–6000MHz. MAIN + DIV, no GNSS antenna. Removable plastic antenna strips on opposite shell sides; reserve 100 × 25 × 8 each. | [Manufacturer](https://www.taoglas.com/product/fxub63-ultra-wide-band-flex-antenna/) |
| 1 | AQEX **qUPS-P-SC-1.3** supercap HAT, evaluation option | Manufacturer family envelope 65 × 56 × 23 plus 11mm header; reserve 80 × 70 × 40. Mount away from Pi on replaceable cassette using engineered power/signal harness. Check revision drawing before making its adapter plate. | [Manufacturer](https://www.aqex.eu/qups-p-sc-raspberry-pi-ups-hat-with-supercapacitor.html), [maker listing](https://lectronz.com/products/aqex-qups-p-sc-13-supercap-ups-hat-raspberry-pi) |
| 1 | Adafruit **3082**, aluminium Pi heatsink | 15 × 15 × 15. Use supplied thermal adhesive and removable retention provision that does not press adjacent components. Leave vertical airflow and a replaceable upper cooling panel. | [Manufacturer](https://www.adafruit.com/product/3082) |
| 1 | Hologram physical SIM | Fit the S121 SIM socket; removable tray gives access without disassembling Pi. Reuse existing service for initial testing. | Existing project LTE record |
| 2 | StarTech **USB31AC50CM**, 0.5m USB-A–C data cable | One SSD host cable, one assumed USB-A glovebox–Pi cable for unbuffered R1. Keep cable exits replaceable; actual car may require C–C. Route SSD cable in broad retained loops; not an unmodeled straight segment. | [Manufacturer datasheet](https://media.startech.com/cms/pdfs/usb31ac50cm_datasheet.pdf) |
| 1 | S121 supplied right-angle USB-A–micro-B cable | Modem data cable. Power configuration must follow S121 schematic; do not parallel independently powered VBUS connections. | S121 datasheet above |
| 1 set | Custom low-voltage harness R1 | Display: 8-conductor 3.3V SPI, 150mm service length; buttons: 3-conductor, 150mm. Select JST-XH 2.5mm locking wire-to-board interfaces on added breakout boards (not claimed to match stock module headers). Power: 20AWG stranded, strain-relieved, keyed connector sized for measured current; no Dupont jumpers for supply current. Detailed wiring follows electrical integration. | Fabricated part specification |
| 1 set | Ruthex **RX-M3x5.7** inserts + ISO 4762 M3 screws | Start with 24 inserts, 12 M3×8 and 12 M3×10 screws as prototype stock; final installed counts come from CAD. Tune pilot using printed coupon. | [Manufacturer](https://www.ruthex.de/en/collections/gewindeeinsatze/products/ruthex-gewindeeinsatz-m3-100-stuck-rx-m3x5-7-messing-gewindebuchsen) |
| 1 set | Board hardware / shell stock | 12 M2.5×6 screws, 12 M2.5 nuts, 8 nylon M2.5×6 standoffs; ASA filament; 1mm clear polycarbonate lens blank 35×35; two 10mm-wide silicone retention straps; two 15mm hook-and-loop mounting straps. These are prototype stock quantities, not final installed counts. | Fabricated/standard parts |

## Flexibility built into the mechanical design

1. **Two-piece shell with removable trays.** M3 shell/tray fasteners; separate M2.5 adapters for boards. No permanent array of speculative board-specific bosses in the base. Tray adapter footprints can change without changing the shell.
2. **Separate power cassette.** Leave its volume available when unpopulated. Accommodate a replacement through a new adapter, or an enclosure-height parameter if larger; do not claim any arbitrary UPS will fit.
3. **Replaceable connector panel.** A clamped cable exit in R1 avoids depending on a USB bulkhead's signal integrity and supports either car-side connector. Provide strain relief on the jacket. Service ports remain accessible with the panel removed.
4. **Replaceable display/button bezel.** Window and switch layout belong to a small panel, with a blank panel option. Mechanically retain the polycarbonate lens. Lid harness gets disconnects and a service loop.
5. **SSD cradle supports 125×40×18mm contents.** Sliding end stop and two straps; separately reserve cable plug and bend volume. A smaller bare bridge or USB SSD can use a replacement cradle later.
6. **Antenna strips are plastic and replaceable.** Start opposite one another, away from heatsinks/SSD and the Pi antenna corner. Reserve 10mm provisional metal clearance; RF tuning and installed-car tests determine actual acceptable clearance and orientation.
7. **Parametric fit allowances.** Start wall 2.4mm, floor 3mm, lid 2.4mm, lid clearance 0.3mm per side, component clearance 1mm per side, cable corridor 25mm and USB cable bend radius 20mm. These are printer/packaging assumptions, not guaranteed cable specs.
8. **Serviceable mounting.** Strap slots and a removable glovebox mounting shoe. No snap-fit-only lid or adhesive-only heavy component retention. SIM, screws and connectors have access paths.

## Electrical and thermal boundary of this selection

The qUPS family lists **2.5A maximum load** and **2A charging current**. It is not selected to power the simultaneous full Pi+modem+SSD load. Evaluate it on the Pi/SSD rail, shed modem power on input loss, and begin shutdown immediately. Actual current, charge limiting and hold-up time determine whether it stays. Its input-threshold adjustment is not assumed to be an input-current limiter. In particular, do not connect a depleted, charging UPS to the Tesla port until total input current is controlled and tested.

The final buffered harness needs upstream VBUS isolation, preserved USB D+/D− and ground, correct Type-C connection behavior, power-loss indication and a shutdown controller integration. An ordinary splitter or cable with a wire removed is not an established solution. This is a separate electrical assembly; the CAD reserve allows work to proceed without pretending it is complete. Unbuffered R1 retains the existing direct Tesla–Pi connection and durable spool behavior, with abrupt-loss tests still required.

S121 is specified at up to 3A input and −25…+70°C. Its modem supply should not be assumed to fit inside the Pi's aggregate 1.2A USB peripheral budget. Determine the approved external-power/USB arrangement from its schematic before connection. The selected SSD lists 3W operation; this is not a complete assembly peak-current measurement. Preserve metered LTE routing while adapting the ASR-specific management path.

Industrial SSD temperature rating does not qualify the Sabrent bridge, Pi, LCD, UPS or completed device at 70°C ambient. Preserve replaceable thermal parts and test the assembled unit. Display and physical confirmation firmware remain implementation tasks.

## First CAD pass

Use `components.json` as the envelope register. Model labelled occupied volumes and distinct cable/keep-out volumes first, then arrange the trays and establish the actual outer dimensions. The JSON does not define a proven collision-free placement. Import manufacturer STEP/drawings when available (S121 has published STEP), and replace approximate envelopes incrementally. Deliver an assembly, exploded view, STEP and individual printable parts after geometry checks. Neither the existing SCAD cutouts nor a reservation box is a manufacturer-accurate model.

The former $139 production estimate is superseded for this prototype selection. Budget allowance: **$350–500 for one complete instrumented prototype**, excluding tools, tax, shipping and service. This is an engineering allowance, not a collected supplier quote; reuse of existing parts can reduce spend. Cost reduction follows fit/power/thermal results.
