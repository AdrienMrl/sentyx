// ble-probe — a headless CoreBluetooth central that exercises the Sentyx Pi's
// onboarding GATT service exactly the way the mobile app does.
//
// It is the BLE half of scripts/healthcheck-unit.sh: on a freshly flashed unit
// it proves that the radio advertises, that a Just Works encrypted link can be
// established (the app's notion of "paired"), and that the Pi can drive its
// Wi-Fi radio on our behalf — none of which is observable from an SSH check,
// because on a fresh unit there is no network to SSH over yet.
//
// The flow mirrors app code path for path (see
// sentyx-app/.../data/ble/{PiBleSession,BlePairingService,BleDeviceWifiService}.kt):
//
//   scan(service UUID) -> connect -> read DeviceInfo (plain)
//   -> subscribe Status (encrypted: forces Just Works pairing)
//   -> write begin_pair|begin_manage -> await state=authenticated
//      (notify + 1s poll-read, one reconnect+retry on the bonding drop)
//   -> subscribe WifiResult -> framed {op:status} -> framed {op:scan}
//   -> optional framed {op:connect,ssid,psk}
//
// Output is line-oriented for the shell wrapper: HC-OK / HC-FAIL / HC-WARN /
// HC-INFO, plus one HC-DATA line carrying a JSON summary. Exit status is 0 only
// when no HC-FAIL was emitted.
//
// Build:  swiftc -O -o ble-probe main.swift
// Usage:  ble-probe [--wifi-connect SSID [--wifi-psk PSK]] [--scan-timeout S]
//                   [--expect-ssid SSID] [--skip-wifi]

import CoreBluetooth
import Foundation

// ─────────────────────────────────────────────────────────── GATT contract
// Mirrors internal/blepair/protocol.go and SentyxGatt.kt. CoreBluetooth
// canonicalises to uppercase, so comparisons use CBUUID equality, never strings.
let uuidService = CBUUID(string: "7A65F000-53E1-4B2E-9F5A-1C29B3E60001")
let uuidDeviceInfo = CBUUID(string: "7A65F001-53E1-4B2E-9F5A-1C29B3E60001")
let uuidControl = CBUUID(string: "7A65F002-53E1-4B2E-9F5A-1C29B3E60001")
let uuidStatus = CBUUID(string: "7A65F003-53E1-4B2E-9F5A-1C29B3E60001")
let uuidConfig = CBUUID(string: "7A65F004-53E1-4B2E-9F5A-1C29B3E60001")
let uuidWifiCmd = CBUUID(string: "7A65F005-53E1-4B2E-9F5A-1C29B3E60001")
let uuidWifiResult = CBUUID(string: "7A65F006-53E1-4B2E-9F5A-1C29B3E60001")
let uuidHealth = CBUUID(string: "7A65F007-53E1-4B2E-9F5A-1C29B3E60001")

// Framing grammar shared with the device (internal/blepair/framing.go). The app
// cannot read the negotiated MTU portably and so always writes 20-byte frames;
// we do the same, which keeps this probe on the app's exact wire behaviour.
let frameCont: UInt8 = 0x00
let frameFirst: UInt8 = 0x01
let frameLast: UInt8 = 0x02
let frameSingle: UInt8 = 0x03
let maxFrame = 20

// ───────────────────────────────────────────────────────────────── output
// print() from the BLE queue is fine (stdout is line-buffered per call here),
// but every emit flushes so a hang still leaves the completed steps on screen.
final class Out {
    static var failures = 0
    static func line(_ tag: String, _ msg: String) {
        print("\(tag) \(msg)")
        fflush(stdout)
    }
    static func ok(_ m: String) { line("HC-OK", m) }
    static func bad(_ m: String) { failures += 1; line("HC-FAIL", m) }
    static func warn(_ m: String) { line("HC-WARN", m) }
    static func info(_ m: String) { line("HC-INFO", m) }
    static func data(_ obj: [String: Any]) {
        guard let d = try? JSONSerialization.data(withJSONObject: obj),
              let s = String(data: d, encoding: .utf8) else { return }
        line("HC-DATA", s)
    }
}

// ─────────────────────────────────────────────────────────────────── args
struct Options {
    var scanTimeout: TimeInterval = 30
    var connectTimeout: TimeInterval = 15
    var opTimeout: TimeInterval = 15
    var wifiConnectTimeout: TimeInterval = 70
    var wifiSSID: String?
    var wifiPSK: String?
    var expectSSID: String?
    var skipWifi = false

    // Whether to exercise the onboarding path the app uses: authenticate a
    // session, then drive the unit's Wi-Fi over BLE. ON by default, because a
    // check that skips it has no business calling a unit green — it was
    // reporting 27 passes on a unit the app could not onboard at all.
    // --no-pair drops back to the plain reads for a quick look.
    var pair = true

    // Enumerate every unit in range instead of probing the first one found.
    // Two units advertising the same service are indistinguishable in a
    // one-shot probe — it silently picks whichever answers first, and a result
    // then describes a device the operator may not even have in mind.
    var list = false

    /// Probe this exact peripheral (CoreBluetooth identifier, as printed by
    /// --list) instead of the first one to answer.
    var device: String?

    /// How long to keep scanning after the first hit before deciding a target
    /// is unambiguous. Cheap insurance against reporting on the wrong unit.
    var settle: TimeInterval = 3
}

func parseArgs() -> Options {
    var o = Options()
    var it = CommandLine.arguments.dropFirst().makeIterator()
    func next(_ flag: String) -> String {
        guard let v = it.next() else {
            Out.bad("\(flag) requires a value")
            exit(2)
        }
        return v
    }
    while let a = it.next() {
        switch a {
        case "--scan-timeout": o.scanTimeout = Double(next(a)) ?? o.scanTimeout
        case "--wifi-connect": o.wifiSSID = next(a)
        case "--wifi-psk": o.wifiPSK = next(a)
        case "--expect-ssid": o.expectSSID = next(a)
        case "--skip-wifi": o.skipWifi = true
        case "--pair": o.pair = true
        case "--no-pair": o.pair = false
        case "--list": o.list = true
        case "--device": o.device = next(a)
        case "-h", "--help":
            print("usage: ble-probe [--scan-timeout S] [--wifi-connect SSID [--wifi-psk PSK]]")
            print("                 [--expect-ssid SSID] [--skip-wifi]")
            exit(0)
        default:
            Out.bad("unknown argument: \(a)")
            exit(2)
        }
    }
    return o
}

// ───────────────────────────────────────────────────────────────── framing
func chunk(_ payload: [UInt8]) -> [Data] {
    let per = maxFrame - 1
    if payload.count <= per { return [Data([frameSingle] + payload)] }
    var frames: [Data] = []
    var off = 0
    while off < payload.count {
        let end = min(off + per, payload.count)
        let header: UInt8 = off == 0 ? frameFirst : (end == payload.count ? frameLast : frameCont)
        frames.append(Data([header] + payload[off..<end]))
        off = end
    }
    return frames
}

/// Accumulates framed notify chunks into one payload; nil while mid-message.
final class Reassembler {
    private var buf = [UInt8]()
    func accept(_ frame: Data) -> [UInt8]? {
        guard let header = frame.first else { return nil }
        let payload = [UInt8](frame.dropFirst())
        switch header {
        case frameSingle: buf = []; return payload
        case frameFirst: buf = payload; return nil
        case frameCont: buf += payload; return nil
        case frameLast:
            let out = buf + payload
            buf = []
            return out
        default: buf = []; return nil
        }
    }
}

// ──────────────────────────────────────────────────────────────── the probe
final class Probe: NSObject, CBCentralManagerDelegate, CBPeripheralDelegate {
    private let opts: Options
    private let queue = DispatchQueue(label: "ble-probe")
    private var central: CBCentralManager!
    private var peripheral: CBPeripheral?
    private var chars: [CBUUID: CBCharacteristic] = [:]

    // Session facts collected for the JSON summary.
    private var summary: [String: Any] = [:]
    private var seen = Set<String>()
    private var candidates: [String: String] = [:]
    private var pending: [String: (CBPeripheral, Int)] = [:]
    private var settleStarted = false
    private var deviceProvisioned = false

    // One outstanding write at a time: every framed write is with-response, so
    // frames must be queued and released by didWriteValueFor. Writing them all
    // up front overruns the ATT queue on some stacks and silently drops frames.
    private var writeQueue: [(CBCharacteristic, Data)] = []
    private var writeInFlight = false

    // Awaited async results, completed from delegate callbacks.
    private var awaitingDeviceInfo: (([String: Any]?) -> Void)?
    private var awaitingHealth: (([String: Any]?) -> Void)?
    private var statusPredicate: (([String: Any]) -> Bool)?
    private var statusDone: (([String: Any]?) -> Void)?
    private var statusPoll: DispatchSourceTimer?
    private var wifiReassembler = Reassembler()
    private var wifiDone: (([String: Any]?) -> Void)?
    private var notifyReady: [CBUUID: () -> Void] = [:]

    private var phaseTimer: DispatchSourceTimer?
    private var pairingRetried = false
    private var finished = false
    private var expectedDisconnect = false

    init(opts: Options) {
        self.opts = opts
        super.init()
    }

    func run() {
        central = CBCentralManager(delegate: self, queue: queue)
        // Belt and braces: if a delegate callback never arrives, don't hang a CI
        // run forever. Every phase also carries its own, tighter deadline.
        let overall = opts.scanTimeout + opts.connectTimeout + 4 * opts.opTimeout
            + (opts.wifiSSID != nil ? opts.wifiConnectTimeout : 0) + 30
        queue.asyncAfter(deadline: .now() + overall) { [weak self] in
            self?.fail("overall probe deadline exceeded")
        }
    }

    // ─────────────────────────────────────────────── phase timing helpers
    private func deadline(_ seconds: TimeInterval, _ what: String) {
        cancelDeadline()
        let t = DispatchSource.makeTimerSource(queue: queue)
        t.schedule(deadline: .now() + seconds)
        t.setEventHandler { [weak self] in self?.fail("timed out: \(what) (\(Int(seconds))s)") }
        t.resume()
        phaseTimer = t
    }

    private func cancelDeadline() {
        phaseTimer?.cancel()
        phaseTimer = nil
    }

    private func fail(_ msg: String) {
        guard !finished else { return }
        Out.bad(msg)
        finish()
    }

    private func finish() {
        guard !finished else { return }
        finished = true
        cancelDeadline()
        statusPoll?.cancel()
        statusPoll = nil
        if let p = peripheral {
            expectedDisconnect = true
            central.cancelPeripheralConnection(p)
        }
        Out.data(summary)
        // Give the disconnect a moment so the Pi's session returns to idle
        // before the next run scans; then exit on the probe's own verdict.
        queue.asyncAfter(deadline: .now() + 0.5) {
            exit(Out.failures == 0 ? 0 : 1)
        }
    }

    // ────────────────────────────────────────────────────── central state
    func centralManagerDidUpdateState(_ c: CBCentralManager) {
        switch c.state {
        case .poweredOn:
            Out.ok("Mac Bluetooth radio powered on")
            startScan()
        case .unauthorized:
            Out.bad("Bluetooth permission denied for this terminal")
            Out.info("grant it in System Settings > Privacy & Security > Bluetooth, then re-run")
            finish()
        case .poweredOff:
            Out.bad("Mac Bluetooth is turned off")
            finish()
        case .unsupported:
            Out.bad("this Mac reports no Bluetooth LE support")
            finish()
        case .resetting:
            Out.warn("Bluetooth stack is resetting; waiting")
        default:
            break
        }
    }

    // ───────────────────────────────────────────────────────────── scan
    private func startScan() {
        // Listing and targeting both mean "look at the air", so neither may be
        // short-circuited by a peripheral that happens to be connected already:
        // that shortcut would silently report on a unit the caller did not ask
        // for, which is the failure this whole option exists to prevent.
        let known = (opts.list || opts.device != nil)
            ? []
            : central.retrieveConnectedPeripherals(withServices: [uuidService])
        if let p = known.first {
            Out.ok("found an already-connected Sentyx peripheral (\(p.name ?? "unnamed"))")
            connect(p)
            return
        }
        Out.info("scanning for service \(uuidService.uuidString)…")
        if opts.list {
            // Duplicates on: a second unit that already advertised once would
            // otherwise never be reported again during the scan window.
            central.scanForPeripherals(withServices: [uuidService],
                                       options: [CBCentralManagerScanOptionAllowDuplicatesKey: true])
            queue.asyncAfter(deadline: .now() + opts.scanTimeout) { [weak self] in
                guard let self else { return }
                Out.ok("\(self.seen.count) unit(s) advertising the onboarding service")
                self.finish()
            }
            return
        }
        deadline(opts.scanTimeout, "no Sentyx unit advertising (BLE radio down, agent not running, or out of range)")
        central.scanForPeripherals(withServices: [uuidService], options: nil)
    }

    func centralManager(_ c: CBCentralManager, didDiscover p: CBPeripheral,
                        advertisementData: [String: Any], rssi RSSI: NSNumber) {
        let id = p.identifier.uuidString
        if opts.list {
            if seen.insert(id).inserted {
                let name = (advertisementData[CBAdvertisementDataLocalNameKey] as? String) ?? p.name ?? "(no name)"
                Out.info("unit \(seen.count): \(id)  \"\(name)\"  \(RSSI) dBm")
            }
            return
        }
        guard peripheral == nil else { return }

        // Targeted run: ignore everything else on the air.
        if let want = opts.device {
            guard id.caseInsensitiveCompare(want) == .orderedSame else {
                candidates[id] = (advertisementData[CBAdvertisementDataLocalNameKey] as? String) ?? p.name ?? "(no name)"
                return
            }
            c.stopScan()
            Out.ok("advertising as \"\(candidates[id] ?? p.name ?? "(no name)")\" (RSSI \(RSSI) dBm)")
            summary["rssi"] = RSSI.intValue
            connect(p)
            return
        }

        // Untargeted: collect for a moment first. Two units in range is not a
        // theoretical case — a bench usually has an old one still powered — and
        // silently probing whichever answers first reports on a device the
        // operator did not mean, which is worse than refusing.
        candidates[id] = (advertisementData[CBAdvertisementDataLocalNameKey] as? String) ?? p.name ?? "(no name)"
        pending[id] = (p, RSSI.intValue)
        if settleStarted { return }
        settleStarted = true
        queue.asyncAfter(deadline: .now() + opts.settle) { [weak self] in self?.chooseTarget() }
        c.stopScan()
        // The Pi puts its LocalName in the SCAN_RSP, and CoreBluetooth delivers
        // that in a *later* callback which duplicate-suppression usually eats —
        // so a missing name here says nothing about the unit. The real name is
        // read back after connecting (peripheral.name), and only reported then.
        let name = (advertisementData[CBAdvertisementDataLocalNameKey] as? String) ?? p.name
        if let name {
            Out.ok("advertising as \"\(name)\" (RSSI \(RSSI) dBm)")
            summary["advName"] = name
        } else {
            Out.ok("advertising the onboarding service (RSSI \(RSSI) dBm)")
        }
        summary["rssi"] = RSSI.intValue
        if RSSI.intValue < -85 {
            Out.warn("weak signal (\(RSSI) dBm) — move the Mac closer if later steps flake")
        }
        connect(p)
    }

    /// Pick the target once the settle window closes, or refuse if the choice
    /// is ambiguous.
    private func chooseTarget() {
        guard peripheral == nil, !finished else { return }
        central.stopScan()
        if pending.count > 1 {
            Out.bad("\(pending.count) units are advertising — refusing to guess which one to check")
            for (id, v) in pending.sorted(by: { $0.value.1 > $1.value.1 }) {
                Out.info("  \(id)  \"\(candidates[id] ?? "?")\"  \(v.1) dBm")
            }
            Out.info("power the others off, or name one: --device <identifier>")
            return finish()
        }
        guard let (p, rssi) = pending.values.first else { return }
        cancelDeadline()
        Out.ok("advertising as \"\(candidates[p.identifier.uuidString] ?? "(no name)")\" (RSSI \(rssi) dBm)")
        summary["rssi"] = rssi
        if rssi < -85 {
            Out.warn("weak signal (\(rssi) dBm) — move the Mac closer if later steps flake")
        }
        connect(p)
    }

    private func connect(_ p: CBPeripheral) {
        peripheral = p
        p.delegate = self
        summary["peripheralId"] = p.identifier.uuidString
        deadline(opts.connectTimeout, "connect to the unit")
        central.connect(p, options: nil)
    }

    func centralManager(_ c: CBCentralManager, didConnect p: CBPeripheral) {
        cancelDeadline()
        Out.ok("GATT connected")
        if summary["advName"] == nil, let n = p.name, !n.isEmpty {
            Out.ok("device name is \"\(n)\" (it advertises the name in the scan response)")
            summary["advName"] = n
        }
        deadline(opts.opTimeout, "service discovery")
        p.discoverServices([uuidService])
    }

    func centralManager(_ c: CBCentralManager, didFailToConnect p: CBPeripheral, error: Error?) {
        let msg = error?.localizedDescription ?? "unknown error"
        // A reflashed unit keeps its Bluetooth address but loses its bonding
        // database, so every host that paired with the old install still holds
        // a key the unit can no longer match. The failure names the symptom,
        // not the cure, and it is not something the unit can fix from its side.
        if msg.localizedCaseInsensitiveContains("pairing information") {
            Out.bad("stale pairing: this Mac holds a bond from before the unit was reflashed")
            Out.info("forget it in System Settings > Bluetooth (or: brew install blueutil && blueutil --unpair <addr>)")
            Out.info("phones that paired with the old install need the same — expect this after every reflash")
            finish()
            return
        }
        fail("could not connect: \(msg)")
    }

    func centralManager(_ c: CBCentralManager, didDisconnectPeripheral p: CBPeripheral, error: Error?) {
        guard !finished, !expectedDisconnect else { return }
        // The first encrypted access triggers Just Works pairing, and several
        // BlueZ builds drop the link at that exact moment. The app reconnects
        // once and retries; a health check that failed here would report a bug
        // the product does not actually have.
        if !pairingRetried, statusDone != nil || awaitingDeviceInfo != nil {
            pairingRetried = true
            Out.warn("link dropped during bonding — reconnecting once (the app does the same)")
            chars.removeAll()
            statusPoll?.cancel(); statusPoll = nil
            statusDone = nil; statusPredicate = nil; awaitingDeviceInfo = nil
            writeQueue.removeAll(); writeInFlight = false
            deadline(opts.connectTimeout, "reconnect after bonding")
            central.connect(p, options: nil)
            return
        }
        fail("the unit disconnected: \(error?.localizedDescription ?? "no error given")")
    }

    // ─────────────────────────────────────────────────────── discovery
    func peripheral(_ p: CBPeripheral, didDiscoverServices error: Error?) {
        if let e = error { return fail("service discovery failed: \(e.localizedDescription)") }
        guard let svc = p.services?.first(where: { $0.uuid == uuidService }) else {
            return fail("the unit does not expose the Sentyx onboarding service")
        }
        Out.ok("onboarding service present")
        p.discoverCharacteristics(
            [uuidDeviceInfo, uuidControl, uuidStatus, uuidConfig, uuidWifiCmd, uuidWifiResult, uuidHealth],
            for: svc)
    }

    func peripheral(_ p: CBPeripheral, didDiscoverCharacteristicsFor service: CBService, error: Error?) {
        cancelDeadline()
        if let e = error { return fail("characteristic discovery failed: \(e.localizedDescription)") }
        for c in service.characteristics ?? [] { chars[c.uuid] = c }
        let required: [(CBUUID, String)] = [
            (uuidDeviceInfo, "device-info"), (uuidControl, "control"), (uuidStatus, "status"),
            (uuidConfig, "config"), (uuidWifiCmd, "wifi-cmd"), (uuidWifiResult, "wifi-result"),
        ]
        let missing = required.filter { chars[$0.0] == nil }.map { $0.1 }
        if !missing.isEmpty {
            return fail("missing characteristics: \(missing.joined(separator: ", ")) — agent is older than this check")
        }
        Out.ok("all 6 onboarding characteristics present")
        readDeviceInfo()
    }

    // ─────────────────────────────────────────────────── step: DeviceInfo
    private func readDeviceInfo() {
        guard let p = peripheral, let c = chars[uuidDeviceInfo] else { return }
        deadline(opts.opTimeout, "read DeviceInfo")
        awaitingDeviceInfo = { [weak self] obj in
            guard let self, let obj else { return self?.fail("DeviceInfo was not valid JSON") ?? () }
            self.cancelDeadline()
            let id = obj["deviceId"] as? String ?? "?"
            let hw = obj["hw"] as? String ?? "?"
            let agent = obj["agent"] as? String ?? "?"
            let state = obj["state"] as? String ?? "?"
            self.deviceProvisioned = obj["provisioned"] as? Bool ?? false
            self.summary["deviceId"] = id
            self.summary["hw"] = hw
            self.summary["agent"] = agent
            self.summary["provisioned"] = self.deviceProvisioned
            Out.ok("DeviceInfo readable: id=\(id) hw=\(hw) agent=\(agent) state=\(state)")
            if self.deviceProvisioned {
                Out.warn("unit is already provisioned — this is not a first-boot unit")
            } else {
                Out.ok("unit is unprovisioned, awaiting onboarding (expected after a fresh flash)")
            }
            // Health can arrive two ways. Inline in DeviceInfo is the one that
            // works everywhere: hosts cache a peripheral's characteristic list
            // against an address that survives reflashing, so a Mac that met an
            // older install never discovers a characteristic added since.
            if let inline = obj["health"] as? [String: Any] {
                self.reportHealth(inline, via: "DeviceInfo")
                if self.opts.pair { self.subscribeStatus() } else { self.finish() }
                return
            }
            if self.opts.pair {
                self.subscribeStatus()
            } else {
                self.readHealth()
            }
        }
        p.readValue(for: c)
    }

    // ───────────────────────────────────── step: health (no pairing at all)
    private func readHealth() {
        guard let p = peripheral, let c = chars[uuidHealth] else {
            // Not a warning: without this characteristic the run cannot say
            // anything about the unit's Wi-Fi, which is the failure that made a
            // unit un-onboardable. A health check that goes green while
            // silently skipping its most important check is worse than a red
            // one, so a missing characteristic fails the run outright.
            Out.bad("no plain health characteristic — this unit's Wi-Fi cannot be checked without pairing")
            Out.info("the agent on the unit predates it: redeploy with scripts/install-agent.sh deploy")
            return finish()
        }
        deadline(opts.opTimeout + 10, "read the health characteristic")
        awaitingHealth = { [weak self] obj in
            guard let self else { return }
            self.cancelDeadline()
            guard let obj else { return self.fail("health characteristic was not valid JSON") }
            self.reportHealth(obj, via: "the health characteristic")
            self.finish()
        }
        p.readValue(for: c)
    }

    /// Turn one health summary into pass/fail lines. Shared by both delivery
    /// paths so a unit is judged identically however the data reached us.
    private func reportHealth(_ obj: [String: Any], via source: String) {
        let radio = obj["wifiRadio"] as? Bool ?? false
        let ssids = obj["wifiSsids"] as? Int ?? 0
        let state = obj["wifiState"] as? String ?? "?"
        let bound = obj["gadgetBound"] as? String ?? ""
        let backing = obj["backingMb"] as? Int ?? 0
        let uptime = obj["uptimeSec"] as? Int ?? 0
        let age = obj["wifiAgeSec"] as? Int ?? -1
        summary["health"] = obj

        Out.ok("health read without pairing, via \(source) (uptime \(uptime)s)")

        // The exact failure that made a unit un-onboardable while looking
        // healthy from every other angle: rfkill clean, radio present, and
        // every scan returning an empty list with no error.
        if radio {
            Out.ok("Wi-Fi radio switch is on")
        } else {
            Out.bad("Wi-Fi radio switch is OFF in NetworkManager — the app would see no networks to join")
        }
        if ssids > 0 {
            Out.ok("the Pi's own scan sees \(ssids) network(s)")
        } else {
            Out.bad("the Pi's own scan sees no networks — it cannot be put on Wi-Fi")
        }
        // The unit refreshes this in the background; a stale figure describes a
        // unit that was healthy minutes ago, which is not the same claim.
        if age >= 0 && age > 300 {
            Out.warn("the Wi-Fi figures are \(age)s old — the unit's background refresh may be stuck")
        }
        Out.info("Wi-Fi state: \(state)")

        if bound.isEmpty {
            Out.bad("the mass-storage gadget is not bound to a UDC — the car would see no drive")
        } else {
            Out.ok("gadget bound to \(bound)")
        }
        if backing > 0 {
            Out.ok("backing image present (\(backing / 1024) GB)")
        } else {
            Out.bad("no backing image — first-boot provisioning did not finish")
        }

        reportClock(obj, wifiState: state)
    }

    /// The unit's idea of the time, and whether anything has confirmed it.
    ///
    /// This board has no battery-backed clock: a freshly flashed unit boots
    /// believing the day its image was built, and every HTTPS request it makes
    /// before the first NTP sample fails certificate validation ("not yet
    /// valid"). That surfaced only at the last step of onboarding, worded as a
    /// server problem, and this network-free check never saw it — it does no
    /// TLS of its own. Reading the unit's clock over BLE closes that gap.
    ///
    /// Unsynchronized is NOT a failure while the unit is still offline: it has
    /// had no chance to sync, and the agent waits for the clock before probing.
    /// It is a failure once the unit is on Wi-Fi, where NTP should have landed.
    private func reportClock(_ obj: [String: Any], wifiState: String) {
        guard let synced = obj["clockSynced"] as? Bool else {
            Out.warn("this agent predates the clock check — TLS failures from a wrong clock would look like server errors")
            return
        }
        let unitTime = obj["timeUnixSec"] as? Int ?? 0
        let skew = unitTime > 0 ? Int(Date().timeIntervalSince1970) - unitTime : 0
        let skewDays = abs(skew) / 86_400

        if synced {
            Out.ok("clock synchronized (within \(abs(skew))s of this Mac)")
            return
        }
        let drift = skewDays > 0 ? " — it reads \(skewDays) day(s) off this Mac" : ""
        if wifiState == "connected" {
            Out.bad("the unit is on Wi-Fi but its clock is unsynchronized\(drift) — HTTPS to the backend will fail certificate checks")
        } else {
            Out.warn("clock not yet synchronized\(drift) — expected while offline; it must sync once the unit joins Wi-Fi")
        }
    }

    // ───────────────────────────────────────── step: pair (encrypted link)
    private func subscribeStatus() {
        guard let p = peripheral, let c = chars[uuidStatus] else { return }
        // Status is encrypted: subscribing is what forces Just Works pairing.
        deadline(opts.opTimeout, "subscribe to Status (Just Works pairing)")
        notifyReady[uuidStatus] = { [weak self] in self?.authenticate() }
        p.setNotifyValue(true, for: c)
    }

    private func authenticate() {
        cancelDeadline()
        Out.ok("encrypted link established — Just Works pairing succeeded")
        // begin_manage is the app's entry point on an already-provisioned unit;
        // begin_pair is rejected there, so pick the same op the app would.
        let op = deviceProvisioned ? "begin_manage" : "begin_pair"
        deadline(opts.opTimeout, "\(op) not acknowledged")
        awaitStatus(where: { st in
            (st["state"] as? String) == "authenticated" || (st["ok"] as? Bool) == false
        }, then: { [weak self] st in
            guard let self else { return }
            self.cancelDeadline()
            guard let st else { return self.fail("no Status response to \(op)") }
            if (st["ok"] as? Bool) == false {
                return self.fail("device refused \(op): \(st["detail"] as? String ?? "no detail")")
            }
            Out.ok("session authenticated via \(op)")
            self.summary["authenticated"] = true
            if self.opts.skipWifi {
                Out.info("skipping Wi-Fi checks (--skip-wifi)")
                self.finish()
            } else {
                self.subscribeWifi()
            }
        })
        writeControl(op)
        // The first write to an encrypted characteristic is answered with
        // "Insufficient Encryption" and only *then* triggers pairing. Nothing
        // replays it once the link comes up — the stack reports no error and
        // the request is simply gone — so a lone write leaves both sides
        // waiting: the unit never sees a command, the client never sees a
        // status. Re-sending it after pairing settles is what turns a
        // first-time connection into a completed one.
        for attempt in 1...3 {
            queue.asyncAfter(deadline: .now() + Double(attempt) * 3.0) { [weak self] in
                guard let self, self.statusDone != nil, !self.finished else { return }
                Out.info("re-sending \(op) after encryption came up (attempt \(attempt + 1))")
                self.writeControl(op)
            }
        }
    }

    /// Await a Status matching `pred`, fed by notify AND a 1s poll-read — the
    /// app's belt-and-braces pattern, because a CCCD subscription can die in the
    /// bonding collision while plain reads keep working.
    private func awaitStatus(where pred: @escaping ([String: Any]) -> Bool,
                             then done: @escaping ([String: Any]?) -> Void) {
        statusPredicate = pred
        statusDone = done
        statusPoll?.cancel()
        let t = DispatchSource.makeTimerSource(queue: queue)
        t.schedule(deadline: .now() + 1, repeating: 1)
        t.setEventHandler { [weak self] in
            guard let self, let p = self.peripheral, let c = self.chars[uuidStatus] else { return }
            p.readValue(for: c)
        }
        t.resume()
        statusPoll = t
    }

    private func settleStatus(_ st: [String: Any]?) {
        guard let done = statusDone else { return }
        statusDone = nil
        statusPredicate = nil
        statusPoll?.cancel()
        statusPoll = nil
        done(st)
    }

    private func writeControl(_ op: String) {
        guard let c = chars[uuidControl] else { return }
        let json = "{\"op\":\"\(op)\"}"
        enqueueWrite(c, Data(json.utf8))
    }

    // ───────────────────────────────────────────────── step: Wi-Fi over BLE
    private func subscribeWifi() {
        guard let p = peripheral, let c = chars[uuidWifiResult] else { return }
        deadline(opts.opTimeout, "subscribe to the Wi-Fi result characteristic")
        notifyReady[uuidWifiResult] = { [weak self] in self?.wifiStatus() }
        p.setNotifyValue(true, for: c)
    }

    private func wifiRequest(_ body: [String: Any], timeout: TimeInterval, label: String,
                             then done: @escaping ([String: Any]?) -> Void) {
        guard let c = chars[uuidWifiCmd] else { return }
        var payload = body
        payload["v"] = 1
        guard let data = try? JSONSerialization.data(withJSONObject: payload) else {
            return fail("could not encode the \(label) command")
        }
        deadline(timeout, label)
        wifiReassembler = Reassembler()
        wifiDone = done
        for frame in chunk([UInt8](data)) { enqueueWrite(c, frame) }
    }

    private func settleWifi(_ obj: [String: Any]?) {
        guard let done = wifiDone else { return }
        wifiDone = nil
        cancelDeadline()
        done(obj)
    }

    private func wifiStatus() {
        cancelDeadline()
        Out.ok("Wi-Fi result notifications subscribed")
        wifiRequest(["op": "status"], timeout: opts.opTimeout, label: "Wi-Fi status query") { [weak self] r in
            guard let self else { return }
            guard let r else { return self.fail("no response to the Wi-Fi status command") }
            if (r["ok"] as? Bool) != true {
                Out.bad("Wi-Fi status failed: \(r["detail"] as? String ?? "no detail")")
                return self.finish()
            }
            if let cur = r["current"] as? [String: Any], let ssid = cur["ssid"] as? String {
                Out.ok("Pi is on Wi-Fi \"\(ssid)\" (signal \(cur["signal"] as? Int ?? 0)%)")
                self.summary["currentSSID"] = ssid
            } else {
                Out.info("Pi is not associated with any network yet (expected on a fresh unit)")
            }
            let saved = (r["saved"] as? [[String: Any]] ?? []).compactMap { $0["ssid"] as? String }
            Out.info("saved networks: \(saved.isEmpty ? "none" : saved.joined(separator: ", "))")
            self.wifiScan()
        }
    }

    private func wifiScan() {
        // The scan is the real proof that the Pi's own Wi-Fi radio works: it is
        // the Pi scanning, relayed over BLE, not anything the Mac can see.
        wifiRequest(["op": "scan"], timeout: opts.opTimeout + 10, label: "Wi-Fi scan on the Pi") { [weak self] r in
            guard let self else { return }
            guard let r else { return self.fail("no response to the Wi-Fi scan command") }
            if (r["ok"] as? Bool) != true {
                Out.bad("Wi-Fi scan failed: \(r["detail"] as? String ?? "no detail")")
                return self.finish()
            }
            let nets = r["networks"] as? [[String: Any]] ?? []
            let ssids = nets.compactMap { $0["ssid"] as? String }
            self.summary["scanCount"] = nets.count
            self.summary["scanSSIDs"] = ssids
            if nets.isEmpty {
                Out.bad("the Pi's scan returned no networks — wlan0 is up but sees nothing")
            } else {
                Out.ok("the Pi scanned \(nets.count) network(s): \(ssids.prefix(5).joined(separator: ", "))\(nets.count > 5 ? ", …" : "")")
            }
            if let want = self.opts.expectSSID {
                if ssids.contains(want) {
                    Out.ok("expected network \"\(want)\" is visible to the Pi")
                } else {
                    Out.bad("expected network \"\(want)\" is NOT in the Pi's scan results")
                }
            }
            if let ssid = self.opts.wifiSSID {
                self.wifiJoin(ssid)
            } else {
                self.finish()
            }
        }
    }

    private func wifiJoin(_ ssid: String) {
        var body: [String: Any] = ["op": "connect", "ssid": ssid]
        if let psk = opts.wifiPSK, !psk.isEmpty { body["psk"] = psk }
        Out.info("asking the Pi to join \"\(ssid)\" (can take up to \(Int(opts.wifiConnectTimeout))s)…")
        wifiRequest(body, timeout: opts.wifiConnectTimeout, label: "Wi-Fi join") { [weak self] r in
            guard let self else { return }
            guard let r else { return self.fail("no response to the Wi-Fi connect command") }
            if (r["ok"] as? Bool) == true {
                Out.ok("the Pi joined \"\(ssid)\"")
                self.summary["joinedSSID"] = ssid
            } else {
                Out.bad("the Pi could not join \"\(ssid)\": \(r["detail"] as? String ?? "no detail")")
            }
            self.finish()
        }
    }

    // ──────────────────────────────────────────────────────── write queue
    private func enqueueWrite(_ c: CBCharacteristic, _ d: Data) {
        writeQueue.append((c, d))
        pumpWrites()
    }

    private func pumpWrites() {
        guard !writeInFlight, let p = peripheral, !writeQueue.isEmpty else { return }
        let (c, d) = writeQueue.removeFirst()
        writeInFlight = true
        p.writeValue(d, for: c, type: .withResponse)
    }

    func peripheral(_ p: CBPeripheral, didWriteValueFor c: CBCharacteristic, error: Error?) {
        writeInFlight = false
        if let e = error {
            writeQueue.removeAll()
            return fail("write to \(c.uuid.uuidString) failed: \(e.localizedDescription)")
        }
        pumpWrites()
    }

    // ──────────────────────────────────────────────────── notify / reads
    func peripheral(_ p: CBPeripheral, didUpdateNotificationStateFor c: CBCharacteristic, error: Error?) {
        if let e = error {
            return fail("could not subscribe to \(c.uuid.uuidString): \(e.localizedDescription) "
                + "(an encryption error here means Just Works pairing was refused)")
        }
        guard c.isNotifying, let cont = notifyReady.removeValue(forKey: c.uuid) else { return }
        cont()
    }

    func peripheral(_ p: CBPeripheral, didUpdateValueFor c: CBCharacteristic, error: Error?) {
        if let e = error {
            // Poll-reads are best-effort; only a failure with nothing pending is fatal.
            if c.uuid == uuidStatus, statusDone != nil { return }
            return fail("read of \(c.uuid.uuidString) failed: \(e.localizedDescription)")
        }
        guard let value = c.value else { return }
        switch c.uuid {
        case uuidDeviceInfo:
            let cb = awaitingDeviceInfo
            awaitingDeviceInfo = nil
            cb?(try? JSONSerialization.jsonObject(with: value) as? [String: Any])
        case uuidHealth:
            let cb = awaitingHealth
            awaitingHealth = nil
            cb?(try? JSONSerialization.jsonObject(with: value) as? [String: Any])
        case uuidStatus:
            guard let st = (try? JSONSerialization.jsonObject(with: value)) as? [String: Any] else { return }
            if let step = st["step"] as? String {
                Out.info("connection-test step \(step): ok=\(st["ok"] as? Bool ?? false)")
            }
            if let pred = statusPredicate, pred(st) { settleStatus(st) }
        case uuidWifiResult:
            guard let payload = wifiReassembler.accept(value) else { return }
            let obj = (try? JSONSerialization.jsonObject(with: Data(payload))) as? [String: Any]
            settleWifi(obj)
        default:
            break
        }
    }
}

// ────────────────────────────────────────────────────────────────── main
let probe = Probe(opts: parseArgs())
probe.run()
RunLoop.main.run()
