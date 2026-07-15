//go:build linux

package health

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// collect gathers all device metrics for one heartbeat. Every sub-collector is
// best-effort and independent: a failure leaves its field(s) nil so the metric
// is omitted, and never aborts the others or the heartbeat.
func collect(storagePath string) metrics {
	var m metrics
	collectStorage(&m, storagePath)
	collectTemp(&m)
	collectThrottled(&m)
	collectWifi(&m)
	collectUptime(&m)
	return m
}

// collectStorage fills free/total bytes for the filesystem backing storagePath
// (the spool/copy-to dir) via statfs. syscall.Statfs_t is available on
// linux/arm64, so no golang.org/x/sys dependency is needed.
func collectStorage(m *metrics, storagePath string) {
	if storagePath == "" {
		return
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(storagePath, &st); err != nil {
		return
	}
	bsize := int64(st.Bsize)
	free := int64(st.Bavail) * bsize
	total := int64(st.Blocks) * bsize
	m.storageFreeBytes = &free
	m.storageTotalBytes = &total
}

// collectTemp reads the SoC temperature from thermal_zone0 (millidegrees C).
func collectTemp(m *metrics) {
	data, err := os.ReadFile("/sys/class/thermal/thermal_zone0/temp")
	if err != nil {
		return
	}
	milli, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return
	}
	c := float64(milli) / 1000.0
	m.cpuTempC = &c
}

// collectThrottled shells out to `vcgencmd get_throttled` (Raspberry Pi
// firmware tool). If vcgencmd is absent all three throttle fields stay nil.
func collectThrottled(m *metrics) {
	path, err := exec.LookPath("vcgencmd")
	if err != nil {
		return
	}
	out, err := exec.Command(path, "get_throttled").Output()
	if err != nil {
		return
	}
	val, ok := parseThrottled(string(out))
	if !ok {
		return
	}
	hex := "0x" + strconv.FormatUint(val, 16)
	now := val&(1<<throttleUnderVoltageNowBit) != 0
	ever := val&(1<<throttleUnderVoltageEverBit) != 0
	m.throttledHex = &hex
	m.underVoltageNow = &now
	m.underVoltageEver = &ever
}

// collectWifi reads the current RSSI from /proc/net/wireless and the SSID from
// iwgetid/nmcli. If the device is not associated to a Wi-Fi network (no data
// line in /proc/net/wireless) all Wi-Fi fields stay nil.
func collectWifi(m *metrics) {
	rssi, ok := readWirelessRSSI()
	if !ok {
		return
	}
	m.wifiRssiDbm = &rssi
	pct := rssiToPct(rssi)
	m.wifiSignalPct = &pct
	if ssid, ok := readSSID(); ok {
		m.wifiSsid = &ssid
	}
}

// readWirelessRSSI parses the signal level (dBm) from the first data line of
// /proc/net/wireless. Columns: "iface: status link level noise ...". The level
// column often has a trailing dot (e.g. "-55.").
func readWirelessRSSI() (int, bool) {
	data, err := os.ReadFile("/proc/net/wireless")
	if err != nil {
		return 0, false
	}
	lines := strings.Split(string(data), "\n")
	// First two lines are headers.
	for _, line := range lines[min(2, len(lines)):] {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, ":") {
			continue
		}
		// Drop the "iface:" prefix, then split remaining numeric columns.
		rest := line[strings.IndexByte(line, ':')+1:]
		fields := strings.Fields(rest)
		if len(fields) < 3 {
			continue
		}
		level := strings.TrimSuffix(fields[2], ".")
		f, err := strconv.ParseFloat(level, 64)
		if err != nil {
			continue
		}
		return int(f), true
	}
	return 0, false
}

// readSSID returns the current Wi-Fi SSID via iwgetid, falling back to nmcli.
func readSSID() (string, bool) {
	if path, err := exec.LookPath("iwgetid"); err == nil {
		if out, err := exec.Command(path, "-r").Output(); err == nil {
			if ssid := strings.TrimSpace(string(out)); ssid != "" {
				return ssid, true
			}
		}
	}
	if path, err := exec.LookPath("nmcli"); err == nil {
		if out, err := exec.Command(path, "-t", "-f", "active,ssid", "dev", "wifi").Output(); err == nil {
			for _, line := range strings.Split(string(out), "\n") {
				if strings.HasPrefix(line, "yes:") {
					if ssid := strings.TrimSpace(strings.TrimPrefix(line, "yes:")); ssid != "" {
						return ssid, true
					}
				}
			}
		}
	}
	return "", false
}

// collectUptime reads the first field of /proc/uptime (seconds since boot).
func collectUptime(m *metrics) {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return
	}
	f, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return
	}
	sec := int64(f)
	m.uptimeSec = &sec
}
