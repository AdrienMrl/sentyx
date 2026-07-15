package wifi

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// fakeRunner dispatches on the joined nmcli args and records every call, so
// tests can drive parsing and assert cleanup side effects.
type fakeRunner struct {
	respond func(args []string) (string, string, error)
	calls   [][]string
}

func (f *fakeRunner) run(ctx context.Context, name string, args ...string) (string, string, error) {
	f.calls = append(f.calls, args)
	if name != "nmcli" {
		return "", "", errors.New("unexpected command: " + name)
	}
	return f.respond(args)
}

func (f *fakeRunner) called(prefix string) bool {
	for _, c := range f.calls {
		if strings.HasPrefix(strings.Join(c, " "), prefix) {
			return true
		}
	}
	return false
}

func TestStatus(t *testing.T) {
	fr := &fakeRunner{respond: func(args []string) (string, string, error) {
		key := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(key, "-t -f ACTIVE,SSID,SIGNAL dev wifi"):
			// The active AP plus a couple of neighbours; SSID with an escaped colon.
			return "no:Neighbour:40\nyes:Home\\:Net:72\nno:Cafe:55\n", "", nil
		case strings.HasPrefix(key, "-t -f NAME,TYPE,ACTIVE,AUTOCONNECT connection show"):
			return "Home\\:Net:802-11-wireless:yes:yes\n" +
				"Wired connection 1:802-3-ethernet:no:yes\n" +
				"Old AP:802-11-wireless:no:no\n", "", nil
		}
		return "", "", errors.New("unexpected args: " + key)
	}}
	m := &nmcli{run: fr.run}

	res, err := m.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Current == nil || res.Current.SSID != "Home:Net" || res.Current.Signal != 72 {
		t.Fatalf("current = %+v", res.Current)
	}
	want := []SavedNetwork{
		{SSID: "Home:Net", Active: true, Autoconnect: true},
		{SSID: "Old AP", Active: false, Autoconnect: false},
	}
	if !reflect.DeepEqual(res.Saved, want) {
		t.Fatalf("saved = %+v, want %+v", res.Saved, want)
	}
}

func TestStatusNotConnected(t *testing.T) {
	fr := &fakeRunner{respond: func(args []string) (string, string, error) {
		if strings.HasPrefix(strings.Join(args, " "), "-t -f ACTIVE,SSID,SIGNAL") {
			return "no:Cafe:55\n", "", nil
		}
		return "", "", nil
	}}
	m := &nmcli{run: fr.run}
	res, err := m.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Current != nil {
		t.Fatalf("current = %+v, want nil", res.Current)
	}
	if res.Saved == nil {
		t.Fatal("saved must be non-nil so it marshals as []")
	}
}

func TestScan(t *testing.T) {
	fr := &fakeRunner{respond: func(args []string) (string, string, error) {
		key := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(key, "-t -f NAME,TYPE connection show"):
			return "Home:802-11-wireless\nWired connection 1:802-3-ethernet\n", "", nil
		case strings.HasPrefix(key, "-t -f SSID,SIGNAL,SECURITY dev wifi list"):
			return "Home:60:WPA2\n" +
				"Home:80:WPA2\n" + // duplicate, stronger — should win
				"Open Cafe:50:\n" +
				"Corp:90:WPA2 802.1X\n" +
				":30:WPA2\n" + // hidden SSID — dropped
				"Legacy:20:WEP\n", "", nil
		}
		return "", "", errors.New("unexpected args: " + key)
	}}
	m := &nmcli{run: fr.run}

	nets, err := m.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Network{
		{SSID: "Corp", Signal: 90, Security: "other", Saved: false},
		{SSID: "Home", Signal: 80, Security: "wpa-psk", Saved: true},
		{SSID: "Open Cafe", Signal: 50, Security: "open", Saved: false},
		{SSID: "Legacy", Signal: 20, Security: "other", Saved: false},
	}
	if !reflect.DeepEqual(nets, want) {
		t.Fatalf("scan = %+v, want %+v", nets, want)
	}
}

func TestScanCap(t *testing.T) {
	fr := &fakeRunner{respond: func(args []string) (string, string, error) {
		if strings.HasPrefix(strings.Join(args, " "), "-t -f SSID,SIGNAL,SECURITY") {
			var b strings.Builder
			for i := 0; i < 50; i++ {
				b.WriteString("Net")
				b.WriteByte(byte('A' + i%26))
				b.WriteString(string(rune('0' + i/26)))
				b.WriteString(":50:WPA2\n")
			}
			return b.String(), "", nil
		}
		return "", "", nil
	}}
	m := &nmcli{run: fr.run}
	nets, err := m.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(nets) != maxScanResults {
		t.Fatalf("scan returned %d networks, want cap of %d", len(nets), maxScanResults)
	}
}

func TestConnectOpenSuccess(t *testing.T) {
	fr := &fakeRunner{respond: func(args []string) (string, string, error) {
		key := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(key, "-t -f NAME,TYPE connection show"):
			return "", "", nil
		case strings.HasPrefix(key, "dev wifi connect Cafe"):
			return "Device 'wlan0' successfully activated", "", nil
		}
		return "", "", errors.New("unexpected args: " + key)
	}}
	m := &nmcli{run: fr.run}
	if err := m.Connect(context.Background(), "Cafe", ""); err != nil {
		t.Fatal(err)
	}
	if fr.called("connection delete") {
		t.Fatal("a successful connect must not delete any profile")
	}
	// Open network: no password argument.
	if fr.called("dev wifi connect Cafe password") {
		t.Fatal("open connect must not pass a password")
	}
}

func TestConnectWithPSK(t *testing.T) {
	fr := &fakeRunner{respond: func(args []string) (string, string, error) {
		if strings.HasPrefix(strings.Join(args, " "), "dev wifi connect Home password hunter2") {
			return "", "", nil
		}
		return "", "", nil
	}}
	m := &nmcli{run: fr.run}
	if err := m.Connect(context.Background(), "Home", "hunter2"); err != nil {
		t.Fatal(err)
	}
	if !fr.called("dev wifi connect Home password hunter2") {
		t.Fatal("secured connect must pass the password")
	}
}

func TestConnectFailureCleansUpNewProfile(t *testing.T) {
	fr := &fakeRunner{respond: func(args []string) (string, string, error) {
		key := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(key, "-t -f NAME,TYPE connection show"):
			return "Wired connection 1:802-3-ethernet\n", "", nil // Home not saved yet
		case strings.HasPrefix(key, "dev wifi connect"):
			return "", "Error: Connection activation failed: (7) Secrets were required, but not provided.", errors.New("exit status 4")
		case strings.HasPrefix(key, "connection delete"):
			return "", "", nil
		}
		return "", "", errors.New("unexpected args: " + key)
	}}
	m := &nmcli{run: fr.run}
	err := m.Connect(context.Background(), "Home", "wrong")
	if err == nil || err.Error() != "wrong password" {
		t.Fatalf("connect error = %v, want wrong password", err)
	}
	if !fr.called("connection delete Home") {
		t.Fatal("failed connect to a new SSID must delete the half-created profile")
	}
}

func TestConnectFailureKeepsExistingProfile(t *testing.T) {
	fr := &fakeRunner{respond: func(args []string) (string, string, error) {
		key := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(key, "-t -f NAME,TYPE connection show"):
			return "Home:802-11-wireless\n", "", nil // Home already saved
		case strings.HasPrefix(key, "dev wifi connect"):
			return "", "Error: No network with SSID 'Home' found.", errors.New("exit status 10")
		}
		return "", "", errors.New("unexpected args: " + key)
	}}
	m := &nmcli{run: fr.run}
	err := m.Connect(context.Background(), "Home", "secret")
	if err == nil || err.Error() != "network not found" {
		t.Fatalf("connect error = %v, want network not found", err)
	}
	if fr.called("connection delete") {
		t.Fatal("failed connect to an existing SSID must keep its saved profile")
	}
}

func TestForget(t *testing.T) {
	fr := &fakeRunner{respond: func(args []string) (string, string, error) {
		if strings.HasPrefix(strings.Join(args, " "), "connection delete Home") {
			return "Connection 'Home' successfully deleted.", "", nil
		}
		return "", "", errors.New("unexpected")
	}}
	m := &nmcli{run: fr.run}
	if err := m.Forget(context.Background(), "Home"); err != nil {
		t.Fatal(err)
	}
}

func TestForgetUnknown(t *testing.T) {
	fr := &fakeRunner{respond: func(args []string) (string, string, error) {
		return "", "Error: unknown connection 'Ghost'.", errors.New("exit status 10")
	}}
	m := &nmcli{run: fr.run}
	err := m.Forget(context.Background(), "Ghost")
	if err == nil || err.Error() != "network not found" {
		t.Fatalf("forget error = %v, want network not found", err)
	}
}

func TestNormalizeSecurity(t *testing.T) {
	cases := map[string]string{
		"":            "open",
		"--":          "open",
		"WPA2":        "wpa-psk",
		"WPA1 WPA2":   "wpa-psk",
		"WPA2 WPA3":   "wpa-psk",
		"WPA2 802.1X": "other",
		"802.1X":      "other",
		"WEP":         "other",
	}
	for in, want := range cases {
		if got := normalizeSecurity(in); got != want {
			t.Errorf("normalizeSecurity(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSplitTerse(t *testing.T) {
	got := splitTerse(`Home\:Net:802-11-wireless:yes`)
	want := []string{"Home:Net", "802-11-wireless", "yes"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("splitTerse = %q, want %q", got, want)
	}
}
