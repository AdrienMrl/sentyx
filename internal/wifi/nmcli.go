package wifi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Per-operation nmcli timeouts. A scan waits for a fresh rescan; a connect
// runs association + DHCP; everything else is a quick local query.
const (
	scanTimeout    = 30 * time.Second
	connectTimeout = 60 * time.Second
	quickTimeout   = 10 * time.Second
)

// The NetworkManager connection type for Wi-Fi profiles, as reported in the
// TYPE column of `nmcli connection show`.
const typeWifi = "802-11-wireless"

// maxScanResults caps the network list handed to the phone; more than this is
// noise on a small screen and bloats the BLE response.
const maxScanResults = 30

// commandRunner executes a command and returns its stdout and stderr
// separately (nmcli puts human-readable failure detail on stderr). Injected so
// tests fake nmcli output without a real system.
type commandRunner func(ctx context.Context, name string, args ...string) (stdout, stderr string, err error)

// nmcli is the NetworkManager-backed Manager.
type nmcli struct {
	run commandRunner
}

// NewNMCLI returns a Manager that shells out to nmcli. Constructing it does no
// I/O and is harmless on any OS; the methods only work where NetworkManager is
// installed (the Pi).
func NewNMCLI() Manager {
	return &nmcli{run: execRunner}
}

func execRunner(ctx context.Context, name string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func (n *nmcli) Status(ctx context.Context) (StatusResult, error) {
	ctx, cancel := context.WithTimeout(ctx, quickTimeout)
	defer cancel()
	res := StatusResult{Saved: []SavedNetwork{}}

	// Current association: the one visible AP nmcli marks ACTIVE.
	out, errOut, err := n.run(ctx, "nmcli", "-t", "-f", "ACTIVE,SSID,SIGNAL", "dev", "wifi")
	if err != nil {
		return res, nmcliError("listing wifi", errOut, err)
	}
	for _, line := range nonEmptyLines(out) {
		f := splitTerse(line)
		if len(f) < 3 || f[0] != "yes" || f[1] == "" {
			continue
		}
		sig, _ := strconv.Atoi(f[2])
		res.Current = &CurrentNetwork{SSID: f[1], Signal: sig}
		break
	}

	// Saved Wi-Fi profiles.
	out, errOut, err = n.run(ctx, "nmcli", "-t", "-f", "NAME,TYPE,ACTIVE,AUTOCONNECT", "connection", "show")
	if err != nil {
		return res, nmcliError("listing connections", errOut, err)
	}
	for _, line := range nonEmptyLines(out) {
		f := splitTerse(line)
		if len(f) < 4 || f[1] != typeWifi {
			continue
		}
		res.Saved = append(res.Saved, SavedNetwork{
			SSID:        f[0],
			Active:      f[2] == "yes",
			Autoconnect: f[3] == "yes",
		})
	}
	return res, nil
}

func (n *nmcli) Scan(ctx context.Context) ([]Network, error) {
	ctx, cancel := context.WithTimeout(ctx, scanTimeout)
	defer cancel()

	// Saved names, so scan entries can be flagged as already known. A failure
	// here is non-fatal: the scan still returns, just without saved flags.
	saved := map[string]bool{}
	if out, _, err := n.run(ctx, "nmcli", "-t", "-f", "NAME,TYPE", "connection", "show"); err == nil {
		for _, line := range nonEmptyLines(out) {
			f := splitTerse(line)
			if len(f) >= 2 && f[1] == typeWifi {
				saved[f[0]] = true
			}
		}
	}

	out, errOut, err := n.run(ctx, "nmcli", "-t", "-f", "SSID,SIGNAL,SECURITY", "dev", "wifi", "list", "--rescan", "yes")
	if err != nil {
		return nil, nmcliError("scanning", errOut, err)
	}
	// Dedupe by SSID keeping the strongest signal; drop empty/hidden SSIDs.
	best := map[string]Network{}
	for _, line := range nonEmptyLines(out) {
		f := splitTerse(line)
		if len(f) < 3 || f[0] == "" {
			continue
		}
		sig, _ := strconv.Atoi(f[1])
		if ex, ok := best[f[0]]; ok && ex.Signal >= sig {
			continue
		}
		best[f[0]] = Network{SSID: f[0], Signal: sig, Security: normalizeSecurity(f[2]), Saved: saved[f[0]]}
	}
	nets := make([]Network, 0, len(best))
	for _, net := range best {
		nets = append(nets, net)
	}
	sort.Slice(nets, func(i, j int) bool {
		if nets[i].Signal != nets[j].Signal {
			return nets[i].Signal > nets[j].Signal
		}
		return nets[i].SSID < nets[j].SSID
	})
	if len(nets) > maxScanResults {
		nets = nets[:maxScanResults]
	}
	return nets, nil
}

func (n *nmcli) Connect(ctx context.Context, ssid, psk string) error {
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()

	// Remember whether this SSID was already saved: only a profile this
	// attempt creates should be cleaned up on failure.
	existed := n.savedWifiExists(ctx, ssid)

	// Length only, never the secret: a join that fails on a password the user
	// is certain of is otherwise indistinguishable from one mangled in transit,
	// and the framing between app and agent is the obvious suspect.
	log.Printf("wifi: connect %q (psk %d chars)", ssid, len(psk))

	args := []string{"dev", "wifi", "connect", ssid}
	if psk != "" {
		args = append(args, "password", psk)
	}
	if _, errOut, err := n.run(ctx, "nmcli", args...); err != nil {
		// A failed connect must not accumulate junk profiles; delete the one
		// nmcli half-created for this attempt (fresh context, since ctx may be
		// the one that just timed out). A pre-existing profile is left intact
		// so the previous network keeps working.
		if !existed {
			dctx, dcancel := context.WithTimeout(context.Background(), quickTimeout)
			n.run(dctx, "nmcli", "connection", "delete", ssid)
			dcancel()
		}
		return connectError(errOut, err)
	}
	return nil
}

func (n *nmcli) Forget(ctx context.Context, ssid string) error {
	ctx, cancel := context.WithTimeout(ctx, quickTimeout)
	defer cancel()
	_, errOut, err := n.run(ctx, "nmcli", "connection", "delete", ssid)
	if err != nil {
		low := strings.ToLower(errOut)
		if strings.Contains(low, "unknown connection") || strings.Contains(low, "not found") {
			return errors.New("network not found")
		}
		if s := strings.TrimSpace(errOut); s != "" {
			return errors.New(summarize(s))
		}
		return fmt.Errorf("forget failed: %v", err)
	}
	return nil
}

// savedWifiExists reports whether a saved Wi-Fi profile named ssid exists.
func (n *nmcli) savedWifiExists(ctx context.Context, ssid string) bool {
	out, _, err := n.run(ctx, "nmcli", "-t", "-f", "NAME,TYPE", "connection", "show")
	if err != nil {
		return false
	}
	for _, line := range nonEmptyLines(out) {
		f := splitTerse(line)
		if len(f) >= 2 && f[1] == typeWifi && f[0] == ssid {
			return true
		}
	}
	return false
}

// normalizeSecurity collapses nmcli's SECURITY column (e.g. "", "WPA2",
// "WPA2 WPA3", "WPA2 802.1X", "WEP") to the three values the app expects.
func normalizeSecurity(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || s == "--" {
		return "open"
	}
	up := strings.ToUpper(s)
	switch {
	case strings.Contains(up, "802.1X"):
		return "other"
	case strings.Contains(up, "WPA"):
		return "wpa-psk"
	default:
		return "other"
	}
}

// connectError turns an nmcli connect failure into a human-readable message.
func connectError(stderr string, err error) error {
	msg := strings.TrimSpace(stderr)
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "secrets were required"),
		strings.Contains(low, "no secrets"),
		strings.Contains(low, "802-11-wireless-security"):
		return errors.New("wrong password")
	case strings.Contains(low, "no network with ssid"),
		strings.Contains(low, "not found"):
		return errors.New("network not found")
	case msg != "":
		return errors.New(summarize(msg))
	default:
		return fmt.Errorf("connect failed: %v", err)
	}
}

// nmcliError wraps a query failure, preferring nmcli's stderr over the raw
// exec error.
func nmcliError(action, stderr string, err error) error {
	if s := strings.TrimSpace(stderr); s != "" {
		return fmt.Errorf("%s: %s", action, summarize(s))
	}
	return fmt.Errorf("%s: %v", action, err)
}

// summarize reduces multi-line nmcli output to its first line without the
// "Error: " prefix.
func summarize(s string) string {
	line := s
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		line = s[:i]
	}
	return strings.TrimPrefix(strings.TrimSpace(line), "Error: ")
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

// splitTerse splits one `nmcli -t` line into fields on unescaped colons,
// unescaping the backslash escapes nmcli applies to ':' and '\' inside values.
func splitTerse(line string) []string {
	var fields []string
	var cur strings.Builder
	for i := 0; i < len(line); i++ {
		switch c := line[i]; {
		case c == '\\' && i+1 < len(line):
			cur.WriteByte(line[i+1])
			i++
		case c == ':':
			fields = append(fields, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	fields = append(fields, cur.String())
	return fields
}
