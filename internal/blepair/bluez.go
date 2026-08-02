package blepair

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/godbus/dbus/v5"
)

// D-Bus object paths for the exported GATT application. BlueZ discovers the
// tree via ObjectManager.GetManagedObjects on appPath.
const (
	appPath     = dbus.ObjectPath("/com/sentyx/onboard")
	servicePath = dbus.ObjectPath("/com/sentyx/onboard/service0")
	advPath     = dbus.ObjectPath("/com/sentyx/onboard/advertisement0")
	agentPath   = dbus.ObjectPath("/com/sentyx/onboard/agent")

	ifaceObjectManager = "org.freedesktop.DBus.ObjectManager"
	ifaceProperties    = "org.freedesktop.DBus.Properties"
	ifaceGattService   = "org.bluez.GattService1"
	ifaceGattChar      = "org.bluez.GattCharacteristic1"
	ifaceAdvertisement = "org.bluez.LEAdvertisement1"
	ifaceAgent         = "org.bluez.Agent1"
	ifaceDevice        = "org.bluez.Device1"
	ifaceAdapter       = "org.bluez.Adapter1"
)

// characteristic is one exported GATT characteristic. BlueZ calls ReadValue/
// WriteValue/StartNotify over D-Bus; handlers run on godbus's dispatch
// goroutine.
type characteristic struct {
	path   dbus.ObjectPath
	uuid   string
	flags  []string
	read   func() []byte
	write  func([]byte) error
	server *gattServer
}

// ReadValue implements org.bluez.GattCharacteristic1.ReadValue.
func (c *characteristic) ReadValue(options map[string]dbus.Variant) ([]byte, *dbus.Error) {
	if c.read == nil {
		return nil, dbus.NewError("org.bluez.Error.NotPermitted", nil)
	}
	v := c.read()
	// Long reads: anything larger than the negotiated MTU minus one arrives as
	// a Read Request followed by Read Blob Requests, and BlueZ passes the byte
	// offset of each. Ignoring it returns the value from the start every time,
	// so a client assembling the blobs gets the head of the value repeated
	// instead of its tail — silent corruption that only appears once a value
	// grows past the MTU, which is exactly what happens as fields are added.
	if off, ok := options["offset"].Value().(uint16); ok && off > 0 {
		if int(off) >= len(v) {
			return []byte{}, nil
		}
		v = v[off:]
	}
	c.server.logf("blepair: gatt: read %s -> %d bytes", c.uuid, len(v))
	return v, nil
}

// WriteValue implements org.bluez.GattCharacteristic1.WriteValue.
func (c *characteristic) WriteValue(value []byte, options map[string]dbus.Variant) *dbus.Error {
	if c.write == nil {
		return dbus.NewError("org.bluez.Error.NotPermitted", nil)
	}
	c.server.logf("blepair: gatt: write %s (%d bytes)", c.uuid, len(value))
	if err := c.write(value); err != nil {
		c.server.logf("blepair: gatt: write to %s rejected: %v", c.uuid, err)
		return dbus.NewError("org.bluez.Error.Failed", []any{err.Error()})
	}
	return nil
}

// StartNotify / StopNotify implement subscription control; delivery happens
// via PropertiesChanged emissions in notify().
func (c *characteristic) StartNotify() *dbus.Error { c.server.setNotifying(c, true); return nil }
func (c *characteristic) StopNotify() *dbus.Error  { c.server.setNotifying(c, false); return nil }

func (c *characteristic) properties() map[string]dbus.Variant {
	return map[string]dbus.Variant{
		"UUID":    dbus.MakeVariant(c.uuid),
		"Service": dbus.MakeVariant(servicePath),
		"Flags":   dbus.MakeVariant(c.flags),
	}
}

// gattServer owns the D-Bus connection and the exported object tree.
type gattServer struct {
	conn    *dbus.Conn
	adapter dbus.ObjectPath
	name    string // advertised LocalName
	logf    func(format string, args ...any)
	chars   []*characteristic

	notifying map[dbus.ObjectPath]bool
	legacyAdv atomic.Bool // advertising via the btmgmt fallback, not bluetoothd
	connected atomic.Bool // a central is currently connected (per watchDisconnects)
	advMu     sync.Mutex  // serializes btmgmt rm-adv/add-adv cycles
}

func newGattServer(conn *dbus.Conn, adapter string, name string, logf func(string, ...any)) *gattServer {
	return &gattServer{
		conn:      conn,
		adapter:   dbus.ObjectPath("/org/bluez/" + adapter),
		name:      name,
		logf:      logf,
		notifying: map[dbus.ObjectPath]bool{},
	}
}

func (g *gattServer) setNotifying(c *characteristic, on bool) {
	g.logf("blepair: gatt: notifying(%s) = %v", c.uuid, on)
	g.notifying[c.path] = on
	// Notifying is a characteristic property; announce the change.
	g.conn.Emit(c.path, ifaceProperties+".PropertiesChanged",
		ifaceGattChar, map[string]dbus.Variant{"Notifying": dbus.MakeVariant(on)}, []string{})
}

// notify pushes a value to subscribed centrals by emitting PropertiesChanged
// for the characteristic's Value property — the BlueZ peripheral-side notify
// mechanism.
func (g *gattServer) notify(path dbus.ObjectPath, value []byte) {
	if err := g.conn.Emit(path, ifaceProperties+".PropertiesChanged",
		ifaceGattChar, map[string]dbus.Variant{"Value": dbus.MakeVariant(value)}, []string{}); err != nil {
		g.logf("blepair: gatt: notify: %v", err)
	}
}

// GetManagedObjects implements org.freedesktop.DBus.ObjectManager for the
// application tree; BlueZ calls it once during RegisterApplication.
func (g *gattServer) GetManagedObjects() (map[dbus.ObjectPath]map[string]map[string]dbus.Variant, *dbus.Error) {
	objs := map[dbus.ObjectPath]map[string]map[string]dbus.Variant{
		servicePath: {
			ifaceGattService: {
				"UUID":    dbus.MakeVariant(UUIDService),
				"Primary": dbus.MakeVariant(true),
			},
		},
	}
	for _, c := range g.chars {
		objs[c.path] = map[string]map[string]dbus.Variant{ifaceGattChar: c.properties()}
	}
	return objs, nil
}

// propsHandler serves org.freedesktop.DBus.Properties.Get/GetAll for one
// exported object from a static property map.
type propsHandler struct {
	iface string
	get   func() map[string]dbus.Variant
}

func (p *propsHandler) Get(iface, prop string) (dbus.Variant, *dbus.Error) {
	if iface != p.iface {
		return dbus.Variant{}, dbus.NewError("org.freedesktop.DBus.Error.InvalidArgs", nil)
	}
	v, ok := p.get()[prop]
	if !ok {
		return dbus.Variant{}, dbus.NewError("org.freedesktop.DBus.Error.InvalidArgs", nil)
	}
	return v, nil
}

func (p *propsHandler) GetAll(iface string) (map[string]dbus.Variant, *dbus.Error) {
	if iface != p.iface {
		return nil, dbus.NewError("org.freedesktop.DBus.Error.InvalidArgs", nil)
	}
	return p.get(), nil
}

func (p *propsHandler) Set(iface, prop string, value dbus.Variant) *dbus.Error {
	return dbus.NewError("org.freedesktop.DBus.Error.PropertyReadOnly", nil)
}

// advertisement implements org.bluez.LEAdvertisement1.
type advertisement struct {
	name string
	logf func(format string, args ...any)
}

func (a *advertisement) Release() *dbus.Error {
	a.logf("blepair: advertisement released by BlueZ")
	return nil
}

func (a *advertisement) properties() map[string]dbus.Variant {
	// Deliberately minimal: extra properties (e.g. Discoverable) require
	// extended-advertising controller support and fail with Invalid
	// Parameters on the Pi 4's Cypress radio.
	return map[string]dbus.Variant{
		"Type":         dbus.MakeVariant("peripheral"),
		"ServiceUUIDs": dbus.MakeVariant([]string{UUIDService}),
		"LocalName":    dbus.MakeVariant(a.name),
	}
}

// pairingAgent implements org.bluez.Agent1 with NoInputNoOutput capability so
// Just Works pairing completes without any Pi-side interaction.
type pairingAgent struct {
	logf func(format string, args ...any)
}

func (a *pairingAgent) Release() *dbus.Error { return nil }
func (a *pairingAgent) RequestPinCode(device dbus.ObjectPath) (string, *dbus.Error) {
	return "", dbus.NewError("org.bluez.Error.Rejected", nil)
}
func (a *pairingAgent) DisplayPinCode(device dbus.ObjectPath, pincode string) *dbus.Error { return nil }
func (a *pairingAgent) RequestPasskey(device dbus.ObjectPath) (uint32, *dbus.Error) {
	return 0, dbus.NewError("org.bluez.Error.Rejected", nil)
}
func (a *pairingAgent) DisplayPasskey(device dbus.ObjectPath, passkey uint32, entered uint16) *dbus.Error {
	return nil
}
func (a *pairingAgent) RequestConfirmation(device dbus.ObjectPath, passkey uint32) *dbus.Error {
	a.logf("blepair: agent: auto-confirming Just Works pairing for %s", device)
	return nil
}
func (a *pairingAgent) RequestAuthorization(device dbus.ObjectPath) *dbus.Error {
	a.logf("blepair: agent: authorizing %s", device)
	return nil
}
func (a *pairingAgent) AuthorizeService(device dbus.ObjectPath, uuid string) *dbus.Error {
	return nil
}
func (a *pairingAgent) Cancel() *dbus.Error { return nil }

// export publishes the application tree, agent, and advertisement objects on
// the bus (methods + Properties per object).
func (g *gattServer) export(adv *advertisement, agent *pairingAgent) error {
	type export struct {
		path  dbus.ObjectPath
		iface string
		obj   any
		props func() map[string]dbus.Variant
	}
	exports := []export{
		{appPath, ifaceObjectManager, g, nil},
		{servicePath, ifaceGattService, struct{}{}, func() map[string]dbus.Variant {
			return map[string]dbus.Variant{
				"UUID":    dbus.MakeVariant(UUIDService),
				"Primary": dbus.MakeVariant(true),
			}
		}},
		{advPath, ifaceAdvertisement, adv, adv.properties},
		{agentPath, ifaceAgent, agent, nil},
	}
	for _, c := range g.chars {
		exports = append(exports, export{c.path, ifaceGattChar, c, c.properties})
	}
	for _, e := range exports {
		if err := g.conn.Export(e.obj, e.path, e.iface); err != nil {
			return fmt.Errorf("blepair: exporting %s: %w", e.path, err)
		}
		if e.props != nil {
			if err := g.conn.Export(&propsHandler{iface: e.iface, get: e.props}, e.path, ifaceProperties); err != nil {
				return fmt.Errorf("blepair: exporting %s properties: %w", e.path, err)
			}
		}
	}
	return nil
}

func (g *gattServer) adapterObj() dbus.BusObject {
	return g.conn.Object("org.bluez", g.adapter)
}

func (g *gattServer) setAdapterProp(prop string, value any) error {
	return g.adapterObj().Call(ifaceProperties+".Set", 0, ifaceAdapter, prop, dbus.MakeVariant(value)).Err
}

// register wires everything into BlueZ: NoInputNoOutput agent, GATT
// application, and advertisement. Registration is retried briefly because
// bluetoothd may still be settling right after boot.
func (g *gattServer) register() error {
	bluez := g.conn.Object("org.bluez", "/org/bluez")
	if err := retry(3, time.Second, func() error {
		return bluez.Call("org.bluez.AgentManager1.RegisterAgent", 0, agentPath, "NoInputNoOutput").Err
	}); err != nil {
		return fmt.Errorf("blepair: registering agent: %w", err)
	}
	if err := bluez.Call("org.bluez.AgentManager1.RequestDefaultAgent", 0, agentPath).Err; err != nil {
		return fmt.Errorf("blepair: default agent: %w", err)
	}
	if err := retry(3, time.Second, func() error {
		return g.adapterObj().Call("org.bluez.GattManager1.RegisterApplication", 0, appPath, map[string]dbus.Variant{}).Err
	}); err != nil {
		return fmt.Errorf("blepair: registering GATT application: %w", err)
	}
	if err := retry(3, time.Second, func() error {
		return g.adapterObj().Call("org.bluez.LEAdvertisingManager1.RegisterAdvertisement", 0, advPath, map[string]dbus.Variant{}).Err
	}); err != nil {
		// Known failure mode on Raspberry Pi OS (kernel 6.12 + BCM43455):
		// bluetoothd's extended-advertising mgmt path is rejected by the
		// kernel with Invalid Parameters for ANY advertisement, while the
		// legacy mgmt Add Advertising op works. Fall back to btmgmt, which
		// uses the legacy op. GATT itself is still served by bluetoothd.
		g.logf("blepair: BlueZ advertisement registration failed (%v); falling back to btmgmt legacy advertising", err)
		// Retry like the D-Bus registrations above: the watchdog that re-asserts
		// a dropped instance only starts once this succeeds, so giving up on the
		// first attempt costs onboarding for the entire boot.
		if err := retry(3, 2*time.Second, g.registerLegacyAdv); err != nil {
			return fmt.Errorf("blepair: registering advertisement (btmgmt fallback): %w", err)
		}
		g.legacyAdv.Store(true)
	}
	return nil
}

// legacyAdvInstance is the mgmt advertising instance the btmgmt fallback
// claims. Fixed: the Pi's onboarding advertisement is the only one expected.
const legacyAdvInstance = "1"

// legacyAdvSettle is the pause between rm-adv and add-adv — see
// registerLegacyAdv for the measured race it avoids.
const legacyAdvSettle = time.Second

// Advertising interval for the legacy path, in the kernel's 0.625 ms units:
// 160 = 100 ms, 240 = 150 ms.
//
// THIS is what makes the Pi discoverable in practice. The kernel initialises
// hdev->le_adv_{min,max}_interval to 0x0800 — 1280 ms — and applies it to mgmt
// advertising instances regardless of their discoverable flag or the adapter's
// Discoverable setting (both tested: neither changes it). At 1280 ms a phone or
// laptop only wins a scan window occasionally: measured 2 detections out of 5
// twelve-second scans. At 100–150 ms it was 5 out of 5, -37 to -50 dBm.
//
// Reachable only through debugfs; there is no mgmt or D-Bus API for it.
const (
	advIntervalPath     = "/sys/kernel/debug/bluetooth"
	advIntervalMinUnits = "160"
	advIntervalMaxUnits = "240"
)

// btmgmtTimeout bounds every btmgmt invocation.
//
// btmgmt is built on BlueZ's bt_shell, which is interactive by design. Under
// systemd there is no tty and stdin is not a terminal, and it has been observed
// to block indefinitely instead of executing and exiting. An unbounded wait
// here is silent and total: it strands the onboarding goroutine between the
// "falling back" log and both the success and failure logs, so the agent looks
// healthy (hci0 up, unit active) while nothing is ever advertised and no error
// is ever reported. Bounding it converts that into a logged failure.
const btmgmtTimeout = 10 * time.Second

// runBtmgmt runs btmgmt with [btmgmtTimeout] and a closed stdin, so it can
// neither wait on terminal input nor hang forever. Returns combined output for
// error messages; a timeout is reported as such, since it means something quite
// different from a non-zero exit.
func (g *gattServer) runBtmgmt(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), btmgmtTimeout)
	defer cancel()

	full := append([]string{"--index", g.adapterIndex()}, args...)
	cmd := exec.CommandContext(ctx, "btmgmt", full...)
	// Explicitly empty (not inherited): bt_shell must see EOF, never a pipe or
	// terminal it might read from.
	cmd.Stdin = strings.NewReader("")
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return out, fmt.Errorf("btmgmt %s: timed out after %s", strings.Join(args, " "), btmgmtTimeout)
	}
	return out, err
}

func (g *gattServer) adapterIndex() string {
	return strings.TrimPrefix(string(g.adapter), "/org/bluez/hci")
}

// registerLegacyAdv advertises via the legacy mgmt API using btmgmt:
// connectable, our service UUID in the advertising data, and the local name
// in the scan response. Also used to re-assert a dead instance: the controller
// sometimes silently stops advertising after an incoming connection while
// `btmgmt advinfo` still lists the instance (kernel/controller desync), so a
// full rm-adv + add-adv cycle is the only reliable restore.
func (g *gattServer) registerLegacyAdv() error {
	g.advMu.Lock()
	defer g.advMu.Unlock()
	// Bracket the btmgmt calls with logs. Without these, a btmgmt that never
	// returns is invisible — the journal simply stops mid-sequence, which is
	// indistinguishable from the goroutine never having run.
	g.logf("blepair: legacy advertising: asserting instance %s via btmgmt", legacyAdvInstance)
	// Must precede add-adv: the kernel snapshots these into the HCI parameters
	// when the instance is enabled.
	g.setAdvInterval()
	// Set Advertising OFF first. This is the load-bearing step, established by
	// on-air A/B measurement on a Pi 4 (BCM43455, kernel 6.12) — with it the
	// instance broadcasts (our UUID visible at -47 dBm); without it the exact
	// same rm-adv/add-adv sequence programs the controller successfully (btmon:
	// ADV_IND + data + Set Advertise Enable, all Status Success, `advinfo` lists
	// the instance) yet NOTHING is emitted. The off forces the kernel to fully
	// tear down its advertising state machine, which bluetoothd's failed
	// extended-advertising registrations (Invalid Parameters on this chip)
	// otherwise leave wedged.
	//
	// Never `advertising on`: that is the adapter-wide Set Advertising mode,
	// which replaces the instance on air with the kernel's default advertisement
	// (ADV_SCAN_IND, non-connectable, random address, no service UUID) — the
	// instance stays listed in `advinfo` while the wrong thing broadcasts.
	if out, err := g.runBtmgmt("advertising", "off"); err != nil {
		g.logf("blepair: legacy advertising: advertising off: %v: %s", err, strings.TrimSpace(string(out)))
	}
	// A previous failed run (or bluetoothd's own failed attempts) can leave a
	// zombie instance behind; clear ours before re-adding. A non-zero exit is
	// expected and ignored (the instance may simply not exist), but a timeout is
	// worth surfacing — it means btmgmt itself is wedged.
	if out, err := g.runBtmgmt("rm-adv", legacyAdvInstance); err != nil {
		g.logf("blepair: legacy advertising: rm-adv: %v: %s", err, strings.TrimSpace(string(out)))
	}
	// rm-adv's teardown is asynchronous in the kernel; an add-adv issued
	// back-to-back races it and loses its HCI enable (also measured on air —
	// without the settle the re-added instance stays dark).
	time.Sleep(legacyAdvSettle)
	if err := g.addLegacyAdv(); err != nil {
		return err
	}
	g.logf("blepair: legacy advertising: instance %s asserted", legacyAdvInstance)
	return nil
}

// setAdvInterval narrows the controller's advertising interval to
// [advIntervalMinUnits]..[advIntervalMaxUnits] — see the constants for why the
// kernel's 1280 ms default makes the device effectively undiscoverable.
//
// Best-effort and non-fatal: debugfs can be unmounted or the files absent on
// other kernels, in which case advertising still works, just slowly. Logged so
// a slow-discovery report has a trail to follow.
func (g *gattServer) setAdvInterval() {
	dir := filepath.Join(advIntervalPath, strings.TrimPrefix(string(g.adapter), "/org/bluez/"))
	for name, value := range map[string]string{
		"adv_min_interval": advIntervalMinUnits,
		"adv_max_interval": advIntervalMaxUnits,
	} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(value+"\n"), 0o644); err != nil {
			g.logf("blepair: legacy advertising: could not set %s (advertising will be slow): %v", p, err)
		}
	}
}

func (g *gattServer) addLegacyAdv() error {
	// Scan response: one Complete Local Name AD structure.
	name := g.name
	if len(name) > 29 {
		name = name[:29]
	}
	scanRsp := fmt.Sprintf("%02x09%x", len(name)+1, name)
	// btmgmt prints nothing without a tty, but its exit code is reliable.
	// -g sets the LE General Discoverable flag in the advertising data, which is
	// what a central expects of a device offering pairing.
	out, err := g.runBtmgmt("add-adv", "-u", UUIDService, "-c", "-g", "-s", scanRsp, legacyAdvInstance)
	if err != nil {
		return fmt.Errorf("btmgmt add-adv: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// reassertLegacyAdv re-registers the btmgmt advertising instance, logging only
// failures (callers decide whether the attempt itself is worth a log line).
func (g *gattServer) reassertLegacyAdv(reason string) {
	if err := g.registerLegacyAdv(); err != nil {
		g.logf("blepair: legacy advertising re-assert (%s) failed: %v", reason, err)
	}
}

// watchLegacyAdv periodically re-asserts the legacy advertising instance for
// the lifetime of ctx, skipping passes while a central is connected. Only
// meaningful on the btmgmt fallback path — when bluetoothd owns the
// advertisement it handles re-advertising itself.
func (g *gattServer) watchLegacyAdv(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// Two gates, because getting this wrong silently breaks
				// pairing: the cached flag, and BlueZ's own view. Re-asserting
				// the advertisement mid-SMP kills the exchange with no error
				// anywhere, so a stale flag must not be the only thing standing
				// between a first-time phone and a working onboarding.
				if g.connected.Load() || g.anyCentralConnected() {
					continue
				}
				// Quiet on success: one log line per minute would be noise.
				g.reassertLegacyAdv("watchdog")
			}
		}
	}()
}

// initiatePairing asks BlueZ to bond with a freshly connected central, from
// the peripheral side.
//
// A central pairing with this unit for the first time gets no further than the
// Pairing Response: it sends its request, the controller answers, and the
// exchange then sits there until the client gives up — no SMP failure, no agent
// callback, nothing in any log. A central that already holds a bond re-pairs
// through the same code path without trouble, which is what made this look like
// a stale-key problem for so long.
//
// Driving the bond from here turns that dead wait into a normal, mutually
// initiated pairing. It is best-effort and asynchronous: Pair() blocks for the
// duration of the exchange, "already exists" is the ordinary answer for a peer
// we know, and a failure must never take down the GATT service.
func (g *gattServer) initiatePairing(path dbus.ObjectPath) {
	go func() {
		call := g.conn.Object("org.bluez", path).Call("org.bluez.Device1.Pair", 0)
		if call.Err != nil {
			// AlreadyExists simply means the peer is already bonded.
			if !strings.Contains(call.Err.Error(), "AlreadyExists") {
				g.logf("blepair: pairing %s: %v", path, call.Err)
			}
			return
		}
		g.logf("blepair: paired with %s", path)
	}()
}

// anyCentralConnected asks BlueZ whether any device is connected right now,
// rather than trusting state we accumulated from signals we might have missed.
func (g *gattServer) anyCentralConnected() bool {
	var objs map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	if err := g.conn.Object("org.bluez", "/").Call(ifaceObjectManager+".GetManagedObjects", 0).Store(&objs); err != nil {
		// Unknown means "maybe": skipping one advertising pass is harmless,
		// interrupting a pairing is not.
		return true
	}
	for _, ifaces := range objs {
		props, isDevice := ifaces[ifaceDevice]
		if !isDevice {
			continue
		}
		if v, ok := props["Connected"]; ok {
			if connected, _ := v.Value().(bool); connected {
				return true
			}
		}
	}
	return false
}

func (g *gattServer) unregister() {
	if g.legacyAdv.Load() {
		// Bounded like the others: teardown must not wedge agent shutdown.
		g.runBtmgmt("rm-adv", legacyAdvInstance)
	}
	g.adapterObj().Call("org.bluez.LEAdvertisingManager1.UnregisterAdvertisement", 0, advPath)
	g.adapterObj().Call("org.bluez.GattManager1.UnregisterApplication", 0, appPath)
	g.conn.Object("org.bluez", "/org/bluez").Call("org.bluez.AgentManager1.UnregisterAgent", 0, agentPath)
}

// watchDisconnects tracks central connections: it invokes onDisconnect when one
// goes away, keeps g.connected current, and on the btmgmt fallback path
// re-asserts the advertisement after each disconnect — the controller does not
// reliably resume the legacy instance on its own.
//
// It listens for InterfacesAdded as well as PropertiesChanged, and that is not
// belt and braces. BlueZ only has a Device1 object for a peer it already knows;
// a central that has never paired with this unit arrives as a *new* object,
// announced by InterfacesAdded with Connected already true, and never emits the
// PropertiesChanged that a Connected-only watcher waits for. g.connected then
// stays false for exactly the peers that are pairing for the first time — and
// the advertising watchdog, which skips passes while a central is connected,
// happily re-asserts the advertisement in the middle of their SMP exchange and
// kills it. The pairing dies with no error on either side: the phone reports a
// write that was never acknowledged, the unit logs nothing at all. It only ever
// bit first-time pairings, which made it look like anything but a race with our
// own watchdog.
func (g *gattServer) watchDisconnects(onDisconnect func()) error {
	if err := g.conn.AddMatchSignal(
		dbus.WithMatchSender("org.bluez"),
		dbus.WithMatchInterface(ifaceProperties),
		dbus.WithMatchMember("PropertiesChanged"),
	); err != nil {
		return err
	}
	if err := g.conn.AddMatchSignal(
		dbus.WithMatchSender("org.bluez"),
		dbus.WithMatchInterface(ifaceObjectManager),
		dbus.WithMatchMember("InterfacesAdded"),
	); err != nil {
		return err
	}
	ch := make(chan *dbus.Signal, 16)
	g.conn.Signal(ch)
	go func() {
		for sig := range ch {
			if len(sig.Body) < 2 {
				continue
			}
			// A device object appearing already connected: the first-pairing
			// case above.
			if sig.Name == ifaceObjectManager+".InterfacesAdded" {
				ifaces, _ := sig.Body[1].(map[string]map[string]dbus.Variant)
				props, isDevice := ifaces[ifaceDevice]
				if !isDevice {
					continue
				}
				if v, ok := props["Connected"]; ok {
					if connected, _ := v.Value().(bool); connected {
						g.connected.Store(true)
						path, _ := sig.Body[0].(dbus.ObjectPath)
						g.logf("blepair: central %s connected (new device)", path)
						g.initiatePairing(path)
					}
				}
				continue
			}
			iface, _ := sig.Body[0].(string)
			if iface != ifaceDevice {
				continue
			}
			changed, _ := sig.Body[1].(map[string]dbus.Variant)
			if v, ok := changed["Connected"]; ok {
				connected, _ := v.Value().(bool)
				g.connected.Store(connected)
				if !connected {
					g.logf("blepair: central %s disconnected", sig.Path)
					onDisconnect()
					if g.legacyAdv.Load() {
						g.logf("blepair: re-asserting legacy advertisement after disconnect")
						g.reassertLegacyAdv("disconnect")
					}
				} else {
					g.logf("blepair: central %s connected", sig.Path)
				}
			}
		}
	}()
	return nil
}

func retry(attempts int, delay time.Duration, fn func() error) error {
	var err error
	for i := 0; i < attempts; i++ {
		if err = fn(); err == nil {
			return nil
		}
		time.Sleep(delay)
	}
	return err
}
