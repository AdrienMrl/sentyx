package blepair

import (
	"fmt"
	"os/exec"
	"strings"
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
	legacyAdv bool // advertising via the btmgmt fallback, not bluetoothd
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

// removeBondedDevices drops every device BlueZ remembers on this adapter.
// A phone that stays bonded to a Pi whose bond store was wiped (or vice
// versa) fails encryption silently, so re-entering pairable mode starts from
// a clean slate. The Pi's Bluetooth is dedicated to onboarding.
func (g *gattServer) removeBondedDevices() {
	var objs map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	if err := g.conn.Object("org.bluez", "/").Call(ifaceObjectManager+".GetManagedObjects", 0).Store(&objs); err != nil {
		g.logf("blepair: listing devices: %v", err)
		return
	}
	for path, ifaces := range objs {
		if _, isDevice := ifaces[ifaceDevice]; !isDevice {
			continue
		}
		if err := g.adapterObj().Call("org.bluez.Adapter1.RemoveDevice", 0, path).Err; err != nil {
			g.logf("blepair: removing stale device %s: %v", path, err)
		} else {
			g.logf("blepair: removed stale bonded device %s", path)
		}
	}
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
		if err := g.registerLegacyAdv(); err != nil {
			return fmt.Errorf("blepair: registering advertisement (btmgmt fallback): %w", err)
		}
		g.legacyAdv = true
	}
	return nil
}

// legacyAdvInstance is the mgmt advertising instance the btmgmt fallback
// claims. Fixed: the Pi's onboarding advertisement is the only one expected.
const legacyAdvInstance = "1"

func (g *gattServer) adapterIndex() string {
	return strings.TrimPrefix(string(g.adapter), "/org/bluez/hci")
}

// registerLegacyAdv advertises via the legacy mgmt API using btmgmt:
// connectable, our service UUID in the advertising data, and the local name
// in the scan response.
func (g *gattServer) registerLegacyAdv() error {
	// A previous failed run (or bluetoothd's own failed attempts) can leave a
	// zombie instance behind; clear ours before re-adding.
	exec.Command("btmgmt", "--index", g.adapterIndex(), "rm-adv", legacyAdvInstance).Run()

	// Scan response: one Complete Local Name AD structure.
	name := g.name
	if len(name) > 29 {
		name = name[:29]
	}
	scanRsp := fmt.Sprintf("%02x09%x", len(name)+1, name)
	// btmgmt prints nothing without a tty, but its exit code is reliable.
	out, err := exec.Command("btmgmt", "--index", g.adapterIndex(),
		"add-adv", "-u", UUIDService, "-c", "-s", scanRsp, legacyAdvInstance).CombinedOutput()
	if err != nil {
		return fmt.Errorf("btmgmt add-adv: %v: %s", err, out)
	}
	return nil
}

func (g *gattServer) unregister() {
	if g.legacyAdv {
		exec.Command("btmgmt", "--index", g.adapterIndex(), "rm-adv", legacyAdvInstance).Run()
	}
	g.adapterObj().Call("org.bluez.LEAdvertisingManager1.UnregisterAdvertisement", 0, advPath)
	g.adapterObj().Call("org.bluez.GattManager1.UnregisterApplication", 0, appPath)
	g.conn.Object("org.bluez", "/org/bluez").Call("org.bluez.AgentManager1.UnregisterAgent", 0, agentPath)
}

// watchDisconnects invokes onDisconnect whenever a Device1 loses its
// connection (Connected -> false PropertiesChanged signal).
func (g *gattServer) watchDisconnects(onDisconnect func()) error {
	if err := g.conn.AddMatchSignal(
		dbus.WithMatchSender("org.bluez"),
		dbus.WithMatchInterface(ifaceProperties),
		dbus.WithMatchMember("PropertiesChanged"),
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
			iface, _ := sig.Body[0].(string)
			if iface != ifaceDevice {
				continue
			}
			changed, _ := sig.Body[1].(map[string]dbus.Variant)
			if v, ok := changed["Connected"]; ok {
				if connected, _ := v.Value().(bool); !connected {
					g.logf("blepair: central %s disconnected", sig.Path)
					onDisconnect()
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
