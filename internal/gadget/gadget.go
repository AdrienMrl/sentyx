// Package gadget configures a USB mass-storage gadget through the Linux
// configfs interface (/sys/kernel/config/usb_gadget), exposing a backing
// image file as a removable USB drive to the host (the car).
//
// Everything is plain filesystem operations (mkdir, write, symlink), so the
// package is unit-testable against a fake configfs tree in a tempdir; on a
// real system it requires the libcomposite module and a UDC (dwc2 on a Pi,
// dummy_hcd in the dev VM).
package gadget

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Config for a Gadget. All fields are required.
type Config struct {
	ConfigFSDir  string // usb_gadget configfs root, e.g. /sys/kernel/config/usb_gadget
	Name         string // gadget directory name, e.g. "teslcam"
	BackingImage string // image file exposed as the mass-storage LUN
	UDC          string // UDC to bind, e.g. "dummy_udc.0" or "fe980000.usb"; see FindUDC
	VendorID     string // USB idVendor, e.g. "0x1d6b" (Linux Foundation)
	ProductID    string // USB idProduct, e.g. "0x0104" (Multifunction Composite Gadget)
	Manufacturer string
	Product      string
	SerialNumber string
}

// Gadget manages one configfs mass-storage gadget.
type Gadget struct {
	cfg Config
}

func New(cfg Config) (*Gadget, error) {
	v := map[string]string{
		"ConfigFSDir": cfg.ConfigFSDir, "Name": cfg.Name, "BackingImage": cfg.BackingImage,
		"UDC": cfg.UDC, "VendorID": cfg.VendorID, "ProductID": cfg.ProductID,
		"Manufacturer": cfg.Manufacturer, "Product": cfg.Product, "SerialNumber": cfg.SerialNumber,
	}
	var missing []string
	for k, s := range v {
		if s == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("gadget: missing required config: %s", strings.Join(missing, ", "))
	}
	if strings.ContainsAny(cfg.Name, "/") {
		return nil, fmt.Errorf("gadget: Name %q must not contain '/'", cfg.Name)
	}
	return &Gadget{cfg: cfg}, nil
}

// dir is the gadget's root directory inside configfs.
func (g *Gadget) dir() string { return filepath.Join(g.cfg.ConfigFSDir, g.cfg.Name) }

// Exists reports whether the gadget directory is already present (e.g. left
// over from a previous run that didn't tear down).
func (g *Gadget) Exists() bool {
	_, err := os.Stat(g.dir())
	return err == nil
}

// Setup creates the gadget in configfs and binds it to the UDC, at which
// point the host sees a removable USB drive backed by BackingImage. Fails if
// the gadget directory already exists (call Teardown first); on any later
// failure the partial gadget is torn down before returning.
func (g *Gadget) Setup() error {
	if _, err := os.Stat(g.cfg.BackingImage); err != nil {
		return fmt.Errorf("gadget: backing image: %w", err)
	}
	if g.Exists() {
		return fmt.Errorf("gadget: %s already exists (tear it down first)", g.dir())
	}
	if err := g.build(); err != nil {
		if terr := g.Teardown(); terr != nil {
			return fmt.Errorf("%w (cleanup also failed: %v)", err, terr)
		}
		return err
	}
	return nil
}

func (g *Gadget) build() error {
	d := g.dir()
	fn := filepath.Join(d, "functions", "mass_storage.0")
	cf := filepath.Join(d, "configs", "c.1")
	steps := []struct {
		mkdir string // directory to create (MkdirAll: real configfs pre-creates some)
		file  string // attribute file to write, relative to the last mkdir'd dir
		value string
	}{
		{mkdir: d},
		{file: filepath.Join(d, "idVendor"), value: g.cfg.VendorID},
		{file: filepath.Join(d, "idProduct"), value: g.cfg.ProductID},
		{mkdir: filepath.Join(d, "strings", "0x409")},
		{file: filepath.Join(d, "strings", "0x409", "manufacturer"), value: g.cfg.Manufacturer},
		{file: filepath.Join(d, "strings", "0x409", "product"), value: g.cfg.Product},
		{file: filepath.Join(d, "strings", "0x409", "serialnumber"), value: g.cfg.SerialNumber},
		{mkdir: fn},
		// stall=0: some hosts (and dummy_hcd) misbehave with endpoint stalls.
		{file: filepath.Join(fn, "stall"), value: "0"},
		// lun.0 is auto-created by real configfs; MkdirAll tolerates that.
		{mkdir: filepath.Join(fn, "lun.0")},
		{file: filepath.Join(fn, "lun.0", "removable"), value: "1"},
		{file: filepath.Join(fn, "lun.0", "file"), value: g.cfg.BackingImage},
		{mkdir: filepath.Join(cf, "strings", "0x409")},
		{file: filepath.Join(cf, "strings", "0x409", "configuration"), value: "Mass Storage"},
		{file: filepath.Join(cf, "MaxPower"), value: "250"},
	}
	for _, s := range steps {
		if s.mkdir != "" {
			if err := os.MkdirAll(s.mkdir, 0o755); err != nil {
				return fmt.Errorf("gadget: %w", err)
			}
			continue
		}
		if err := writeAttr(s.file, s.value); err != nil {
			return err
		}
	}
	if err := os.Symlink(fn, filepath.Join(cf, "mass_storage.0")); err != nil {
		return fmt.Errorf("gadget: %w", err)
	}
	// Binding the UDC is what makes the gadget go live on the bus.
	if err := writeAttr(filepath.Join(d, "UDC"), g.cfg.UDC); err != nil {
		return fmt.Errorf("gadget: binding UDC %q: %w", g.cfg.UDC, err)
	}
	return nil
}

// Teardown unbinds the gadget from the UDC and removes it from configfs.
// Best-effort: it keeps going past individual failures and returns them
// joined, so a partially-created gadget can still be cleaned up.
func (g *Gadget) Teardown() error {
	d := g.dir()
	if !g.Exists() {
		return nil
	}
	var errs []error
	// Unbind first — removing a bound gadget is refused by the kernel. The
	// write fails harmlessly if it was never bound.
	if err := writeAttr(filepath.Join(d, "UDC"), ""); err != nil {
		if bound, rerr := g.boundUDC(); rerr == nil && bound != "" {
			errs = append(errs, err)
		}
	}
	if err := os.Remove(filepath.Join(d, "configs", "c.1", "mass_storage.0")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, err)
	}
	for _, sub := range []string{
		filepath.Join("configs", "c.1", "strings", "0x409"),
		filepath.Join("configs", "c.1"),
		filepath.Join("functions", "mass_storage.0"),
		filepath.Join("strings", "0x409"),
		"",
	} {
		if err := removeDir(filepath.Join(d, sub)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// BoundUDC returns the UDC the gadget is currently bound to ("" if unbound).
func (g *Gadget) BoundUDC() (string, error) { return g.boundUDC() }

func (g *Gadget) boundUDC() (string, error) {
	data, err := os.ReadFile(filepath.Join(g.dir(), "UDC"))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// FindUDC returns the name of the single UDC registered under classDir
// (normally /sys/class/udc). Zero or multiple UDCs is an error: the caller
// must then name one explicitly.
func FindUDC(classDir string) (string, error) {
	entries, err := os.ReadDir(classDir)
	if err != nil {
		return "", fmt.Errorf("gadget: listing UDCs: %w", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	switch len(names) {
	case 0:
		return "", fmt.Errorf("gadget: no UDC found in %s (missing dwc2/dummy_hcd?)", classDir)
	case 1:
		return names[0], nil
	default:
		sort.Strings(names)
		return "", fmt.Errorf("gadget: multiple UDCs in %s (%s): specify one explicitly", classDir, strings.Join(names, ", "))
	}
}

// writeAttr writes a configfs attribute. O_CREATE makes the same code work
// against a fake tree in tests (real configfs attributes always exist).
func writeAttr(path, value string) error {
	if err := os.WriteFile(path, []byte(value+"\n"), 0o644); err != nil {
		return fmt.Errorf("gadget: writing %s: %w", path, err)
	}
	return nil
}

// removeDir removes a configfs directory. On real configfs a plain rmdir
// works even when attribute files or default groups (lun.0) are inside; on a
// fake tree it fails with ENOTEMPTY, so fall back to clearing the children.
func removeDir(path string) error {
	err := os.Remove(path)
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		return err
	}
	entries, rerr := os.ReadDir(path)
	if rerr != nil {
		return err
	}
	for _, e := range entries {
		child := filepath.Join(path, e.Name())
		var cerr error
		if e.IsDir() {
			cerr = removeDir(child)
		} else {
			cerr = os.Remove(child)
		}
		if cerr != nil {
			return cerr
		}
	}
	return os.Remove(path)
}
