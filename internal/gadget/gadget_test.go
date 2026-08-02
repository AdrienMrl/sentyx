package gadget

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testConfig returns a Config rooted in a fake configfs tree, with a backing
// image that actually exists (Setup checks it).
func testConfig(t *testing.T) Config {
	t.Helper()
	tmp := t.TempDir()
	img := filepath.Join(tmp, "backing.img")
	if err := os.WriteFile(img, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return Config{
		ConfigFSDir:  filepath.Join(tmp, "usb_gadget"),
		Name:         "teslcam",
		BackingImage: img,
		UDC:          "dummy_udc.0",
		VendorID:     "0x1d6b",
		ProductID:    "0x0104",
		Manufacturer: "teslcam",
		Product:      "TeslaCam Drive",
		SerialNumber: "0001",
	}
}

func mustNew(t *testing.T, cfg Config) *Gadget {
	t.Helper()
	g, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// readAttr reads a written attribute, stripping the trailing newline.
func readAttr(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return strings.TrimSuffix(string(data), "\n")
}

func TestNewRequiresAllFields(t *testing.T) {
	cfg := testConfig(t)
	cfg.UDC = ""
	cfg.SerialNumber = ""
	_, err := New(cfg)
	if err == nil {
		t.Fatal("expected error for missing fields")
	}
	for _, want := range []string{"SerialNumber", "UDC"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name missing field %s", err, want)
		}
	}
}

func TestNewRejectsSlashInName(t *testing.T) {
	cfg := testConfig(t)
	cfg.Name = "a/b"
	if _, err := New(cfg); err == nil {
		t.Fatal("expected error for '/' in Name")
	}
}

func TestSetupWritesConfigFSTree(t *testing.T) {
	cfg := testConfig(t)
	g := mustNew(t, cfg)
	if err := g.Setup(); err != nil {
		t.Fatal(err)
	}

	d := filepath.Join(cfg.ConfigFSDir, cfg.Name)
	for path, want := range map[string]string{
		"idVendor":                                 cfg.VendorID,
		"idProduct":                                cfg.ProductID,
		"strings/0x409/manufacturer":               cfg.Manufacturer,
		"strings/0x409/product":                    cfg.Product,
		"strings/0x409/serialnumber":               cfg.SerialNumber,
		"functions/mass_storage.0/stall":           "0",
		"functions/mass_storage.0/lun.0/removable": "1",
		"functions/mass_storage.0/lun.0/file":      cfg.BackingImage,
		"configs/c.1/strings/0x409/configuration":  "Mass Storage",
		"configs/c.1/MaxPower":                     "250",
		"UDC":                                      cfg.UDC,
	} {
		if got := readAttr(t, filepath.Join(d, path)); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}

	link := filepath.Join(d, "configs", "c.1", "mass_storage.0")
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("config function symlink: %v", err)
	}
	if want := filepath.Join(d, "functions", "mass_storage.0"); target != want {
		t.Errorf("symlink -> %q, want %q", target, want)
	}

	bound, err := g.BoundUDC()
	if err != nil {
		t.Fatal(err)
	}
	if bound != cfg.UDC {
		t.Errorf("BoundUDC = %q, want %q", bound, cfg.UDC)
	}
}

// The debug functions are what make a unit with no network reachable at all,
// so their wiring is worth pinning: both are linked into the live config, the
// ECM MACs are fixed (the host keys a network service to them), and a teardown
// by a run that does NOT have them enabled still removes everything.
func TestSetupWiresCompositeDebugFunctions(t *testing.T) {
	cfg := testConfig(t)
	cfg.SerialConsole = true
	cfg.USBEthernet = true
	g := mustNew(t, cfg)
	if err := g.Setup(); err != nil {
		t.Fatal(err)
	}
	d := filepath.Join(cfg.ConfigFSDir, cfg.Name)

	for _, fn := range []string{"acm.0", "ecm.0"} {
		link := filepath.Join(d, "configs", "c.1", fn)
		target, err := os.Readlink(link)
		if err != nil {
			t.Fatalf("%s not linked into the config: %v", fn, err)
		}
		if want := filepath.Join(d, "functions", fn); target != want {
			t.Errorf("%s -> %q, want %q", fn, target, want)
		}
	}
	for path, want := range map[string]string{
		"functions/ecm.0/dev_addr":  ecmDevAddr,
		"functions/ecm.0/host_addr": ecmHostAddr,
	} {
		if got := readAttr(t, filepath.Join(d, path)); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}

	// Same gadget, same configfs root — only the debug flags differ.
	plainCfg := cfg
	plainCfg.SerialConsole = false
	plainCfg.USBEthernet = false
	plain := mustNew(t, plainCfg)
	if err := plain.Teardown(); err != nil {
		t.Fatalf("teardown by a run without the debug functions: %v", err)
	}
	if _, err := os.Stat(d); !os.IsNotExist(err) {
		t.Errorf("gadget dir still present after teardown: %v", err)
	}
}

func TestSetupFailsIfBackingImageMissing(t *testing.T) {
	cfg := testConfig(t)
	cfg.BackingImage = filepath.Join(t.TempDir(), "nope.img")
	g := mustNew(t, cfg)
	if err := g.Setup(); err == nil {
		t.Fatal("expected error for missing backing image")
	}
	if g.Exists() {
		t.Error("gadget dir should not have been created")
	}
}

func TestSetupFailsIfGadgetExists(t *testing.T) {
	cfg := testConfig(t)
	g := mustNew(t, cfg)
	if err := g.Setup(); err != nil {
		t.Fatal(err)
	}
	if err := g.Setup(); err == nil {
		t.Fatal("expected error for existing gadget")
	}
}

func TestSetupCleansUpOnBindFailure(t *testing.T) {
	cfg := testConfig(t)
	g := mustNew(t, cfg)
	// Force the final UDC bind to fail: pre-create the gadget dir path such
	// that the UDC attribute is an unwritable directory.
	d := filepath.Join(cfg.ConfigFSDir, cfg.Name)
	if err := os.MkdirAll(filepath.Join(d, "UDC", "block"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Setup refuses an existing dir, so exercise build+cleanup via the same
	// path Setup uses internally.
	err := g.build()
	if err == nil {
		t.Fatal("expected bind failure")
	}
	if terr := g.Teardown(); terr != nil {
		t.Fatalf("teardown after failed build: %v", terr)
	}
	if g.Exists() {
		t.Error("gadget dir still present after teardown")
	}
}

func TestTeardownRemovesEverything(t *testing.T) {
	cfg := testConfig(t)
	g := mustNew(t, cfg)
	if err := g.Setup(); err != nil {
		t.Fatal(err)
	}
	if err := g.Teardown(); err != nil {
		t.Fatal(err)
	}
	if g.Exists() {
		t.Error("gadget dir still present after teardown")
	}
	// The configfs root itself must survive.
	if _, err := os.Stat(cfg.ConfigFSDir); err != nil {
		t.Errorf("configfs root removed: %v", err)
	}
}

func TestTeardownIsIdempotent(t *testing.T) {
	cfg := testConfig(t)
	g := mustNew(t, cfg)
	if err := g.Teardown(); err != nil {
		t.Fatalf("teardown of nonexistent gadget: %v", err)
	}
	if err := g.Setup(); err != nil {
		t.Fatal(err)
	}
	if err := g.Teardown(); err != nil {
		t.Fatal(err)
	}
	if err := g.Teardown(); err != nil {
		t.Fatalf("second teardown: %v", err)
	}
}

func TestSetupAfterTeardownWorks(t *testing.T) {
	cfg := testConfig(t)
	g := mustNew(t, cfg)
	for i := 0; i < 2; i++ {
		if err := g.Setup(); err != nil {
			t.Fatalf("setup #%d: %v", i+1, err)
		}
		if err := g.Teardown(); err != nil {
			t.Fatalf("teardown #%d: %v", i+1, err)
		}
	}
}

func TestFindUDC(t *testing.T) {
	dir := t.TempDir()
	if _, err := FindUDC(dir); err == nil {
		t.Error("expected error for empty UDC class dir")
	}

	if err := os.Mkdir(filepath.Join(dir, "dummy_udc.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	name, err := FindUDC(dir)
	if err != nil {
		t.Fatal(err)
	}
	if name != "dummy_udc.0" {
		t.Errorf("FindUDC = %q, want dummy_udc.0", name)
	}

	if err := os.Mkdir(filepath.Join(dir, "fe980000.usb"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = FindUDC(dir)
	if err == nil {
		t.Fatal("expected error for multiple UDCs")
	}
	for _, want := range []string{"dummy_udc.0", "fe980000.usb"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("multi-UDC error %q does not list %s", err, want)
		}
	}

	if _, err := FindUDC(filepath.Join(dir, "missing")); err == nil {
		t.Error("expected error for missing class dir")
	}
}
